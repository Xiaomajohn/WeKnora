import asyncio
import os
import shutil
import subprocess
import tempfile
import uuid
import zipfile
from email import policy
from email.parser import BytesParser
from pathlib import Path
from urllib.parse import unquote, urlparse

from bs4 import BeautifulSoup
from charset_normalizer import from_bytes
from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from fastapi.responses import JSONResponse, Response

DOCX_MIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
OLE_MAGIC = b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"
ALLOWED_IMAGES = {"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif"}
REQUIRED_DOCX_ENTRIES = {"[Content_Types].xml", "_rels/.rels", "word/document.xml"}
TMP_ROOT = Path(os.getenv("CONVERTER_TMP_DIR", "/tmp/document-converter"))
MAX_FILE_BYTES = int(os.getenv("CONVERTER_MAX_FILE_MB", "50")) * 1024 * 1024
MAX_DECODED_BYTES = int(os.getenv("CONVERTER_MAX_DECODED_MB", "100")) * 1024 * 1024
MAX_OUTPUT_BYTES = int(os.getenv("CONVERTER_MAX_OUTPUT_MB", "50")) * 1024 * 1024
MAX_PARTS = int(os.getenv("CONVERTER_MAX_PARTS", "500"))
MAX_IMAGES = int(os.getenv("CONVERTER_MAX_IMAGES", "100"))
MAX_IMAGE_BYTES = int(os.getenv("CONVERTER_MAX_IMAGE_MB", "10")) * 1024 * 1024
TIMEOUT_SECONDS = int(os.getenv("CONVERTER_TIMEOUT_SECONDS", "45"))
MAX_CONCURRENCY = int(os.getenv("CONVERTER_MAX_CONCURRENCY", "2"))

app = FastAPI(title="WeKnora Document Converter", docs_url=None, redoc_url=None)
slots = asyncio.Semaphore(MAX_CONCURRENCY)


class ConversionError(Exception):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message


@app.exception_handler(ConversionError)
async def conversion_error_handler(_, exc: ConversionError):
    return JSONResponse(
        status_code=exc.status,
        content={"code": exc.code, "message": exc.message},
    )


def _decode_text(payload: bytes, charset: str | None) -> str:
    if charset:
        try:
            return payload.decode(charset)
        except (LookupError, UnicodeDecodeError):
            pass
    match = from_bytes(payload).best()
    if match is not None:
        return str(match)
    return payload.decode("utf-8", errors="replace")


def _safe_image_name(index: int, content_type: str, raw_name: str) -> str:
    suffix = ALLOWED_IMAGES[content_type]
    candidate = Path(unquote(urlparse(raw_name).path)).name
    stem = Path(candidate).stem if candidate else f"image-{index}"
    clean = "".join(ch if ch.isalnum() or ch in "-_" else "_" for ch in stem)[:80]
    return f"{clean or f'image-{index}'}{suffix}"


def _extract_mhtml(content: bytes, workdir: Path) -> Path:
    if content.startswith(OLE_MAGIC):
        raise ConversionError(400, "real_doc", "文件是真实 DOC，不需要 MHTML 转换")
    try:
        message = BytesParser(policy=policy.default).parsebytes(content)
    except Exception as exc:
        raise ConversionError(400, "invalid_mime", "文件不是有效的 MIME/MHTML") from exc
    if not message.is_multipart() or message.get_content_maintype() != "multipart":
        raise ConversionError(400, "invalid_mime", "文件不是 multipart MIME/MHTML")

    parts = list(message.walk())
    if len(parts) > MAX_PARTS:
        raise ConversionError(413, "too_many_parts", "MIME part 数量超过限制")

    html_candidates: list[tuple[int, str, str]] = []
    image_aliases: dict[str, str] = {}
    total_decoded = 0
    image_count = 0
    assets_dir = workdir / "assets"
    assets_dir.mkdir()

    for part in parts:
        if part.is_multipart():
            continue
        payload = part.get_payload(decode=True) or b""
        total_decoded += len(payload)
        if total_decoded > MAX_DECODED_BYTES:
            raise ConversionError(413, "decoded_too_large", "MIME 解码后内容超过限制")
        content_type = part.get_content_type().lower()
        disposition = (part.get_content_disposition() or "").lower()
        location = str(part.get("Content-Location", "")).strip()
        content_id = str(part.get("Content-ID", "")).strip("<>")

        if content_type == "text/html" and disposition != "attachment" and payload:
            text = _decode_text(payload, part.get_content_charset())
            visible_length = len(BeautifulSoup(text, "lxml").get_text(" ", strip=True))
            html_candidates.append((visible_length, text, location))
        elif content_type in ALLOWED_IMAGES and payload:
            image_count += 1
            if image_count > MAX_IMAGES:
                raise ConversionError(413, "too_many_images", "内嵌图片数量超过限制")
            if len(payload) > MAX_IMAGE_BYTES:
                raise ConversionError(413, "image_too_large", "单张内嵌图片超过限制")
            name = _safe_image_name(image_count, content_type, location or content_id)
            path = assets_dir / name
            path.write_bytes(payload)
            relative = f"assets/{name}"
            for alias in (content_id, f"cid:{content_id}" if content_id else "", location):
                if alias:
                    image_aliases[alias] = relative

    if not html_candidates:
        raise ConversionError(422, "html_missing", "MHTML 中没有可转换的 HTML 正文")
    _, html, _ = max(html_candidates, key=lambda item: item[0])
    soup = BeautifulSoup(html, "lxml")
    for tag in soup.find_all(["script", "iframe", "object", "embed", "form", "link", "base"]):
        tag.decompose()
    for tag in soup.find_all(True):
        for attr in list(tag.attrs):
            if attr.lower().startswith("on"):
                del tag.attrs[attr]
        if tag.name == "img":
            src = str(tag.get("src", "")).strip()
            replacement = image_aliases.get(src)
            if replacement:
                tag["src"] = replacement
            else:
                tag.decompose()
        elif tag.name == "a":
            href = str(tag.get("href", "")).strip()
            parsed = urlparse(href)
            if parsed.scheme.lower() in {"http", "https", "ftp", "file", "data", "javascript"}:
                tag.attrs.pop("href", None)

    html_path = workdir / "index.html"
    html_path.write_text(str(soup), encoding="utf-8")
    return html_path


def _validate_docx(path: Path) -> bytes:
    if not path.is_file():
        raise ConversionError(502, "output_missing", "LibreOffice 未生成 DOCX")
    size = path.stat().st_size
    if size <= 0 or size > MAX_OUTPUT_BYTES:
        raise ConversionError(502, "output_invalid_size", "DOCX 输出大小异常")
    try:
        with zipfile.ZipFile(path) as archive:
            if not REQUIRED_DOCX_ENTRIES.issubset(set(archive.namelist())):
                raise ConversionError(502, "output_invalid", "输出不是有效的 DOCX")
            if archive.testzip() is not None:
                raise ConversionError(502, "output_corrupt", "DOCX 输出已损坏")
    except zipfile.BadZipFile as exc:
        raise ConversionError(502, "output_invalid", "输出不是有效的 DOCX") from exc
    return path.read_bytes()


def _convert_sync(content: bytes) -> bytes:
    TMP_ROOT.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=TMP_ROOT) as tmp:
        workdir = Path(tmp)
        html_path = _extract_mhtml(content, workdir)
        outdir = workdir / "output"
        profile = workdir / "profile"
        outdir.mkdir()
        profile.mkdir()
        command = [
            "soffice", "--headless", "--nologo", "--nodefault",
            "--nofirststartwizard", "--nolockcheck",
            f"-env:UserInstallation={profile.as_uri()}",
            "--convert-to", "docx:MS Word 2007 XML", "--outdir", str(outdir), str(html_path),
        ]
        try:
            result = subprocess.run(
                command,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=TIMEOUT_SECONDS,
                check=False,
                start_new_session=True,
                env={**os.environ, "HOME": str(workdir)},
            )
        except subprocess.TimeoutExpired as exc:
            raise ConversionError(504, "conversion_timeout", "LibreOffice 转换超时") from exc
        if result.returncode != 0:
            raise ConversionError(502, "conversion_failed", "LibreOffice 转换失败")
        return _validate_docx(outdir / "index.docx")


@app.get("/health")
def health():
    TMP_ROOT.mkdir(parents=True, exist_ok=True)
    if shutil.which("soffice") is None or not os.access(TMP_ROOT, os.W_OK):
        raise HTTPException(status_code=503, detail="converter unavailable")
    return {"status": "ok"}


@app.post("/v1/convert/mhtml-to-docx")
async def convert(file: UploadFile = File(...), filename: str | None = Form(None)):
    request_id = str(uuid.uuid4())
    content = await file.read(MAX_FILE_BYTES + 1)
    if not content:
        raise ConversionError(400, "empty_file", "上传文件为空")
    if len(content) > MAX_FILE_BYTES:
        raise ConversionError(413, "file_too_large", "上传文件超过限制")
    try:
        await asyncio.wait_for(slots.acquire(), timeout=0.1)
    except asyncio.TimeoutError as exc:
        raise ConversionError(429, "busy", "转换服务当前繁忙") from exc
    try:
        output = await asyncio.to_thread(_convert_sync, content)
    finally:
        slots.release()
    output_name = f"{Path(filename or file.filename or 'document').stem}.docx"
    return Response(
        output,
        media_type=DOCX_MIME,
        headers={
            "Content-Disposition": f'attachment; filename="{output_name}"',
            "X-Request-ID": request_id,
        },
    )
