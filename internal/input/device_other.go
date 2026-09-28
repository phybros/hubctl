//go:build !linux || (!arm64 && !amd64)

package input

import "fmt"

func Open(string) (*Monitor, error) {
	return nil, fmt.Errorf("touch monitoring requires Linux arm64 or amd64")
}
