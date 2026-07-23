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

	// 关闭 DuckDB 1.5.x 默认的自动安装行为。即便本地已存在扩展文件，
	// INSTALL 也会向 extensions.duckdb.org:80 发请求去验证；在无公网
	// 环境构建会卡到连接超时。关掉后让后续 INSTALL 走 docker/Dockerfile.app
	// 里 curl 预下载的本地副本。
	if _, err := sqlDB.ExecContext(ctx, "SET autoinstall_known_extensions=false;"); err != nil {
		panic(fmt.Errorf("disable autoinstall: %w", err))
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
