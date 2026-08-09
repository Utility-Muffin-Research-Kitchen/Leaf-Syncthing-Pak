package life1

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

type fixtureManifest struct {
	Fixtures []struct {
		Name                   string `json:"name"`
		FrameTotalBytes        int    `json:"frame_total_bytes"`
		LengthPrefixValue      int    `json:"length_prefix_value"`
		SemanticLimitViolation bool   `json:"expect_semantic_ceiling_violation"`
		SHA256                 string `json:"sha256_of_frame"`
	} `json:"fixtures"`
}

type fixtureDescription struct {
	Message          any    `json:"message"`
	CanonicalPayload string `json:"canonical_payload_utf8"`
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..",
		"umrk-workspace", "contracts", "leaf-services", "wire-fixtures"))
}

func readJSON(t *testing.T, path string, dst any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, dst); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalWireFixtures(t *testing.T) {
	root := fixtureRoot(t)
	var manifest fixtureManifest
	readJSON(t, filepath.Join(root, "MANIFEST.json"), &manifest)
	if len(manifest.Fixtures) == 0 {
		t.Fatal("fixture manifest is empty")
	}

	for _, fixture := range manifest.Fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			frame, err := os.ReadFile(filepath.Join(root, fixture.Name+".bin"))
			if err != nil {
				t.Fatal(err)
			}
			if len(frame) != fixture.FrameTotalBytes {
				t.Fatalf("frame size = %d, want %d", len(frame), fixture.FrameTotalBytes)
			}
			if len(frame) < framePrefixBytes {
				t.Fatal("fixture omits frame prefix")
			}
			if got := int(binary.BigEndian.Uint32(frame[:framePrefixBytes])); got != fixture.LengthPrefixValue {
				t.Fatalf("prefix = %d, want %d", got, fixture.LengthPrefixValue)
			}
			digest := sha256.Sum256(frame)
			if got := hex.EncodeToString(digest[:]); got != fixture.SHA256 {
				t.Fatalf("sha256 = %s, want %s", got, fixture.SHA256)
			}

			if fixture.SemanticLimitViolation {
				if _, err := Decode(frame); !errors.Is(err, ErrSemanticLimit) {
					t.Fatalf("Decode() error = %v, want %v", err, ErrSemanticLimit)
				}
				return
			}

			payload, err := Decode(frame)
			if err != nil {
				t.Fatal(err)
			}
			var description fixtureDescription
			readJSON(t, filepath.Join(root, fixture.Name+".json"), &description)
			if string(payload) != description.CanonicalPayload {
				t.Fatal("decoded payload differs from canonical fixture bytes")
			}
			var decodedMessage any
			if err := json.Unmarshal(payload, &decodedMessage); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodedMessage, description.Message) {
				t.Fatal("decoded payload differs from fixture's logical message")
			}
			reencoded, err := Encode(payload)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reencoded, frame) {
				t.Fatal("re-encoded frame differs byte-for-byte")
			}
		})
	}
}

func TestDecodeRejectsMalformedFrames(t *testing.T) {
	transportPrefix := make([]byte, framePrefixBytes)
	binary.BigEndian.PutUint32(transportPrefix, TransportMaxPayload+1)

	tests := []struct {
		name  string
		frame []byte
		want  error
	}{
		{name: "short prefix", frame: []byte{0, 0, 0}, want: ErrFrameTooShort},
		{name: "transport ceiling", frame: transportPrefix, want: ErrTransportLimit},
		{name: "length mismatch", frame: []byte{0, 0, 0, 2, '{'}, want: ErrLengthMismatch},
		{name: "invalid JSON", frame: []byte{0, 0, 0, 1, '{'}, want: ErrInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(test.frame); !errors.Is(err, test.want) {
				t.Fatalf("Decode() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEncodeUsesUTF8ByteLength(t *testing.T) {
	payload := json.RawMessage(`{"reason":"café déjà-vu 🔥"}`)
	frame, err := Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := int(binary.BigEndian.Uint32(frame[:framePrefixBytes])); got != len(payload) {
		t.Fatalf("prefix = %d, want UTF-8 byte length %d", got, len(payload))
	}
}

func TestEncodeRejectsSemanticOversize(t *testing.T) {
	payload := make(json.RawMessage, SemanticMaxPayload+1)
	if _, err := Encode(payload); !errors.Is(err, ErrSemanticLimit) {
		t.Fatalf("Encode() error = %v, want %v", err, ErrSemanticLimit)
	}
}

func TestReadAndWriteHandleShortIO(t *testing.T) {
	payload := json.RawMessage(`{"v":1,"op":"game.state","id":"7"}`)
	var output shortWriter
	if err := Write(&output, payload); err != nil {
		t.Fatal(err)
	}
	decoded, err := Read(&shortReader{reader: bytes.NewReader(output.Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded = %s, want %s", decoded, payload)
	}
}

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(contents []byte) (int, error) {
	if len(contents) > 3 {
		contents = contents[:3]
	}
	return w.Buffer.Write(contents)
}

type shortReader struct{ reader *bytes.Reader }

func (r *shortReader) Read(contents []byte) (int, error) {
	if len(contents) > 2 {
		contents = contents[:2]
	}
	return r.reader.Read(contents)
}
