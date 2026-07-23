package main

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/duckdb/duckdb-go/v2"
)

// duckdbExtensions is the list of DuckDB extensions required by WeKnora's
// data analysis tool. `spatial` is used for layer metadata (st_read_meta)
// so we can enumerate sheet names from Excel files, while `excel` provides
// the dedicated read_xlsx reader with proper type inference.
var duckdbExtensions = []string{"spatial", "excel"}

func downloadExtensions() {
	ctx := context.Background()

	sqlDB, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		panic(err)
	}
	defer sqlDB.Close()

	// 关闭 DuckDB 1.5.x 默认的自动安装/自动更新行为：
	// 即便本地已存在扩展文件，DuckDB 默认仍会向 extensions.duckdb.org:80
	// 发请求去 verify / update。失败直接 panic，构建期在没有公网的环境下
	// 会卡 100s 超时（详见项目记忆 9d777125）。关掉后 INSTALL 直接复用
	// docker/Dockerfile.app 的 curl 预下载产物。
	if _, err := sqlDB.ExecContext(ctx, "SET autoinstall_known_extensions=false;"); err != nil {
		panic(fmt.Errorf("disable autoinstall: %w", err))
	}
	if _, err := sqlDB.ExecContext(ctx, "SET autoupdate_known_extensions=false;"); err != nil {
		panic(fmt.Errorf("disable autoupdate: %w", err))
	}

	for _, ext := range duckdbExtensions {
		if _, err := sqlDB.ExecContext(ctx, fmt.Sprintf("INSTALL %s;", ext)); err != nil {
			panic(fmt.Errorf("failed to install %s extension: %w", ext, err))
		}
		if _, err := sqlDB.ExecContext(ctx, fmt.Sprintf("LOAD %s;", ext)); err != nil {
			panic(fmt.Errorf("failed to load %s extension: %w", ext, err))
		}
	}
}

func main() {
	downloadExtensions()
}
