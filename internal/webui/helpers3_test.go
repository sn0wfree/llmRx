package webui

import "os"

func osWriteFile(path string, data []byte) {
	os.WriteFile(path, data, 0644)
}
