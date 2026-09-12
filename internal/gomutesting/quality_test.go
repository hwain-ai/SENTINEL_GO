package gomutesting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hwain-hwang/sentinel-go/internal/crap"
)

func TestAdapterCallablesStayWithinComplexityBudget(t *testing.T) {
	for _, directory := range []string{".", "../../cmd/sentinel-go-mutesting-probe", "../runner"} {
		files, err := filepath.Glob(filepath.Join(directory, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			payload, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := crap.AnalyzeSource(payload, filepath.Base(file))
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Complexity > 8 {
					t.Errorf("%s:%s complexity=%d", file, row.QualifiedName, row.Complexity)
				}
			}
		}
	}
}

func TestModuleVersionMatchesReportedBackend(t *testing.T) {
	module, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(module), "github.com/avito-tech/go-mutesting "+Version+"\n") {
		t.Fatal("backend version differs from module pin")
	}
}
