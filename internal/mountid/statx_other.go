//go:build !linux

package mountid

func statxIdentity(path string) (StatxInfo, error) {
	_ = path
	return StatxInfo{}, nil
}
