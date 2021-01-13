package generate

import (
	"fmt"
	"os"
	"testing"
)

func TestGenerate(t *testing.T) {
	fmt.Println(os.Getenv("GITHUB_TOKEN"))
	if err := GenerateManifest("r2d4", "ubuntu", "master"); err != nil {
		t.Fatal(err)
	}
}
