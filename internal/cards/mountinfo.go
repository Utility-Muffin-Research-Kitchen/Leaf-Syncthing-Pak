package cards

import (
	"path/filepath"
	"strings"
)

type mountRecord struct {
	writable bool
}

func parseMountInfo(contents []byte) map[string]mountRecord {
	mounts := make(map[string]mountRecord)
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		point := decodeMountInfo(fields[4])
		writable := false
		for _, option := range strings.Split(fields[5], ",") {
			if option == "rw" {
				writable = true
			}
		}
		mounts[filepath.Clean(point)] = mountRecord{writable: writable}
	}
	return mounts
}

func decodeMountInfo(value string) string {
	return strings.NewReplacer(
		`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`,
	).Replace(value)
}
