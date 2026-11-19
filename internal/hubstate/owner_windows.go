package hubstate

import "io/fs"

func checkOwner(string, fs.FileInfo) error { return nil }
