// packzip 极简跨平台压缩工具：把文件打包为 zip（发版脚本用）。
// 起因：bash 里 zip 命令非全平台标配，Windows Git Bash 常无、Linux 最小
// 安装也可能没有；powershell fallback 在 Linux 不可用。构建机必有 Go，
// 用标准库 archive/zip 统一行为。
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "用法: packzip <输出.zip> <文件...>")
		os.Exit(1)
	}
	out, err := os.Create(os.Args[1])
	if err != nil {
		fatal(err)
	}
	defer out.Close()
	w := zip.NewWriter(out)
	for _, path := range os.Args[2:] {
		if err := addFile(w, path); err != nil {
			fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		fatal(err)
	}
}

// addFile 把单个文件加入 zip（条目名取文件名本身，不带目录层级）
func addFile(w *zip.Writer, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	fw, err := w.Create(filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = io.Copy(fw, src)
	return err
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "packzip:", err)
	os.Exit(1)
}
