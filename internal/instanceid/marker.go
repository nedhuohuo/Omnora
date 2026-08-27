package instanceid

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var validMarkerContents = regexp.MustCompile(`^[0-9a-f]{64}\n?$`)

// MarkerPaths identifies the instance marker stored in each persistent volume.
type MarkerPaths struct {
	Config  string
	Data    string
	Managed string
}

// ValidateMarkers returns the shared instance ID after validating all three
// persistent-volume markers. Errors identify only the logical volume and never
// include marker contents or filesystem paths.
func ValidateMarkers(paths MarkerPaths) (string, error) {
	configID, err := readMarker("config", paths.Config)
	if err != nil {
		return "", err
	}
	dataID, err := readMarker("data", paths.Data)
	if err != nil {
		return "", err
	}
	managedID, err := readMarker("managed", paths.Managed)
	if err != nil {
		return "", err
	}
	if configID != dataID || configID != managedID {
		return "", fmt.Errorf("persistent volume instance markers do not match")
	}
	return configID, nil
}

func readMarker(volume, path string) (string, error) {
	file, err := openMarker(path)
	if err != nil {
		return "", fmt.Errorf("%s instance marker cannot be opened", volume)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 66))
	if err != nil {
		return "", fmt.Errorf("%s instance marker cannot be read", volume)
	}
	if !validMarkerContents.Match(contents) {
		return "", fmt.Errorf("%s instance marker is invalid", volume)
	}
	return strings.TrimSuffix(string(contents), "\n"), nil
}

func openMarker(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("instance marker is unavailable")
	}
	file, err := openMarkerNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("instance marker cannot be opened")
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		file.Close()
		return nil, fmt.Errorf("instance marker changed during validation")
	}
	return file, nil
}
