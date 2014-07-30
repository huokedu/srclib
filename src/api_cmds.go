package src

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kr/fs"
	"github.com/sourcegraph/go-sourcegraph/sourcegraph"
	"github.com/sourcegraph/srclib/buildstore"
	"github.com/sourcegraph/srclib/config"
	"github.com/sourcegraph/srclib/graph"
	"github.com/sourcegraph/srclib/grapher"
	"github.com/sourcegraph/srclib/plan"
	"github.com/sourcegraph/srclib/unit"
)

func init() {
	c, err := CLI.AddCommand("api",
		"API",
		"",
		&apiCmd,
	)
	if err != nil {
		log.Fatal(err)
	}

	_, err = c.AddCommand("describe",
		"display documentation for the def under the cursor",
		"Returns information about the definition referred to by the cursor's current position in a file.",
		&apiDescribeCmd,
	)
	if err != nil {
		log.Fatal(err)
	}
}

type APICmd struct{}

var apiCmd APICmd

func (c *APICmd) Execute(args []string) error { return nil }

type APIDescribeCmd struct {
	File      string `long:"file" required:"yes" value-name:"FILE"`
	StartByte int    `long:"start-byte" required:"yes" value-name:"BYTE"`

	Examples bool `long:"examples" describe:"show examples from Sourcegraph.com"`
}

var apiDescribeCmd APIDescribeCmd

func (c *APIDescribeCmd) Execute(args []string) error {
	repo, err := OpenRepo(filepath.Dir(c.File))
	if err != nil {
		return err
	}

	c.File, err = filepath.Rel(repo.RootDir, c.File)
	if err != nil {
		return err
	}

	if err := os.Chdir(repo.RootDir); err != nil {
		return err
	}

	buildStore, err := buildstore.NewRepositoryStore(repo.RootDir)
	if err != nil {
		return err
	}

	configOpt := config.Options{
		Repo:   string(repo.URI()),
		Subdir: ".",
	}
	toolchainExecOpt := ToolchainExecOpt{ExeMethods: "program"}

	// Config & plan repository if not yet built.
	if _, err := buildStore.Stat(buildStore.CommitPath(repo.CommitID)); os.IsNotExist(err) {
		configCmd := &ConfigCmd{
			Options:          configOpt,
			ToolchainExecOpt: toolchainExecOpt,
		}
		if err := configCmd.Execute(nil); err != nil {
			return err
		}

		planCmd := &PlanCmd{
			ToolchainExecOpt: toolchainExecOpt,
		}
		if err := planCmd.Execute(nil); err != nil {
			return err
		}
	}

	// Always re-make.
	//
	// TODO(sqs): optimize this
	makeCmd := &MakeCmd{
		Options:          configOpt,
		ToolchainExecOpt: toolchainExecOpt,
	}
	if err := makeCmd.Execute(nil); err != nil {
		return err
	}

	// TODO(sqs): This whole lookup is totally inefficient. The storage format
	// is not optimized for lookups.

	// Find all source unit definition files.
	var unitFiles []string
	unitSuffix := buildstore.DataTypeSuffix(unit.SourceUnit{})
	w := fs.WalkFS(buildStore.CommitPath(repo.CommitID), buildStore)
	for w.Step() {
		if strings.HasSuffix(w.Path(), unitSuffix) {
			unitFiles = append(unitFiles, w.Path())
		}
	}

	// Find which source units the file belongs to.
	var units []*unit.SourceUnit
	for _, unitFile := range unitFiles {
		var u *unit.SourceUnit
		f, err := buildStore.Open(unitFile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := json.NewDecoder(f).Decode(&u); err != nil {
			return fmt.Errorf("%s: %s", unitFile, err)
		}
		for _, f2 := range u.Files {
			if f2 == c.File {
				units = append(units, u)
				break
			}
		}
	}

	if gopt.Verbose {
		if len(units) > 0 {
			ids := make([]string, len(units))
			for i, u := range units {
				ids[i] = string(u.ID())
			}
			log.Printf("Position %s:%d is in %d source units %v.", c.File, c.StartByte, len(units), ids)
		} else {
			log.Printf("Position %s:%d is not in any source units.", c.File, c.StartByte)
		}
	}

	// Find the ref(s) at the character position.
	var ref *graph.Ref
OuterLoop:
	for _, u := range units {
		var g grapher.Output
		graphFile := buildStore.FilePath(repo.CommitID, plan.SourceUnitDataFilename("graph", u))
		f, err := buildStore.Open(graphFile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := json.NewDecoder(f).Decode(&g); err != nil {
			return fmt.Errorf("%s: %s", graphFile, err)
		}
		for _, ref2 := range g.Refs {
			if c.File == ref2.File && c.StartByte >= ref2.Start && c.StartByte <= ref2.End {
				ref = ref2
				if ref.SymbolUnit == "" {
					ref.SymbolUnit = u.Name
				}
				if ref.SymbolUnitType == "" {
					ref.SymbolUnitType = u.Type
				}
				break OuterLoop
			}
		}
	}

	if ref == nil {
		if gopt.Verbose {
			log.Printf("No ref found at %s:%d.", c.File, c.StartByte)
		}
		return nil
	}

	if ref.SymbolRepo == "" {
		ref.SymbolRepo = repo.URI()
	}

	var resp struct {
		Def      *sourcegraph.Symbol
		Examples []*sourcegraph.Example
	}

	// Now find the def for this ref.
	defInCurrentRepo := ref.SymbolRepo == repo.URI()
	if defInCurrentRepo {
		// Def is in the current repo.
		var g grapher.Output
		graphFile := buildStore.FilePath(repo.CommitID, plan.SourceUnitDataFilename("graph", &unit.SourceUnit{Name: ref.SymbolUnit, Type: ref.SymbolUnitType}))
		f, err := buildStore.Open(graphFile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := json.NewDecoder(f).Decode(&g); err != nil {
			return fmt.Errorf("%s: %s", graphFile, err)
		}
		for _, def2 := range g.Symbols {
			if def2.Path == ref.SymbolPath {
				resp.Def = &sourcegraph.Symbol{Symbol: *def2}
				break
			}
		}
		if resp.Def != nil {
			for _, doc := range g.Docs {
				if doc.Path == ref.SymbolPath {
					resp.Def.DocHTML = doc.Data
				}
			}
		}
		if resp.Def == nil && gopt.Verbose {
			log.Printf("No definition found with path %q in unit %q type %q.", ref.SymbolPath, ref.SymbolUnit, ref.SymbolUnitType)
		}
	}

	spec := sourcegraph.SymbolSpec{
		Repo:     string(ref.SymbolRepo),
		UnitType: ref.SymbolUnitType,
		Unit:     ref.SymbolUnit,
		Path:     string(ref.SymbolPath),
	}

	var wg sync.WaitGroup

	if resp.Def == nil {
		// Def is not in the current repo. Try looking it up using the
		// Sourcegraph API.
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			resp.Def, _, err = apiclient.Symbols.Get(spec, &sourcegraph.SymbolGetOptions{Doc: true})
			if err != nil && gopt.Verbose {
				log.Printf("Couldn't fetch definition %v: %s.", spec, err)
			}
		}()
	}

	if fetchExamples := true; fetchExamples {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			resp.Examples, _, err = apiclient.Symbols.ListExamples(spec, &sourcegraph.SymbolListExamplesOptions{
				Formatted:   true,
				ListOptions: sourcegraph.ListOptions{PerPage: 4},
			})
			if err != nil && gopt.Verbose {
				log.Printf("Couldn't fetch examples for %v: %s.", spec, err)
			}
		}()
	}

	wg.Wait()

	if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
		return err
	}
	return nil
}
