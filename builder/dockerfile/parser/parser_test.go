package parser

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testDir = "testfiles"
const negativeTestDir = "testfiles-negative"
const testFileLineInfo = "testfile-line/Dockerfile"

func getDirs(t *testing.T, dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	defer f.Close()

	dirs, err := f.Readdirnames(0)
	if err != nil {
		t.Fatal(err)
	}

	return dirs
}

func TestTestNegative(t *testing.T) {
	for _, dir := range getDirs(t, negativeTestDir) {
		dockerfile := filepath.Join(negativeTestDir, dir, "Dockerfile")

		df, err := os.Open(dockerfile)
		if err != nil {
			t.Fatalf("Dockerfile missing for %s: %v", dir, err)
		}
		defer df.Close()

		_, _, err = Parse(df)
		if err == nil {
			t.Fatalf("No error parsing broken dockerfile for %s", dir)
		}
	}
}

func TestTestData(t *testing.T) {
	for _, dir := range getDirs(t, testDir) {
		dockerfile := filepath.Join(testDir, dir, "Dockerfile")
		resultfile := filepath.Join(testDir, dir, "result")

		df, err := os.Open(dockerfile)
		if err != nil {
			t.Fatalf("Dockerfile missing for %s: %v", dir, err)
		}
		defer df.Close()

		ast, _, err := Parse(df)
		if err != nil {
			t.Fatalf("Error parsing %s's dockerfile: %v", dir, err)
		}

		content, err := ioutil.ReadFile(resultfile)
		if err != nil {
			t.Fatalf("Error reading %s's result file: %v", dir, err)
		}

		if runtime.GOOS == "windows" {
			// CRLF --> CR to match Unix behavior
			content = bytes.Replace(content, []byte{'\x0d', '\x0a'}, []byte{'\x0a'}, -1)
		}

		if ast.Dump()+"\n" != string(content) {
			fmt.Fprintln(os.Stderr, "Result:\n"+ast.Dump())
			fmt.Fprintln(os.Stderr, "Expected:\n"+string(content))
			t.Fatalf("%s: AST dump of dockerfile does not match result", dir)
		}
	}
}

func TestParseWords(t *testing.T) {
	tests := []map[string][]string{
		{
			"input":  {"foo"},
			"expect": {"foo"},
		},
		{
			"input":  {"foo bar"},
			"expect": {"foo", "bar"},
		},
		{
			"input":  {"foo\\ bar"},
			"expect": {"foo\\ bar"},
		},
		{
			"input":  {"foo=bar"},
			"expect": {"foo=bar"},
		},
		{
			"input":  {"foo bar 'abc xyz'"},
			"expect": {"foo", "bar", "'abc xyz'"},
		},
		{
			"input":  {`foo bar "abc xyz"`},
			"expect": {"foo", "bar", `"abc xyz"`},
		},
		{
			"input":  {"àöû"},
			"expect": {"àöû"},
		},
		{
			"input":  {`föo bàr "âbc xÿz"`},
			"expect": {"föo", "bàr", `"âbc xÿz"`},
		},
	}

	for _, test := range tests {
		words := parseWords(test["input"][0], DefaultDirectives())
		if len(words) != len(test["expect"]) {
			t.Fatalf("length check failed. input: %v, expect: %q, output: %q", test["input"][0], test["expect"], words)
		}
		for i, word := range words {
			if word != test["expect"][i] {
				t.Fatalf("word check failed for word: %q. input: %q, expect: %q, output: %q", word, test["input"][0], test["expect"], words)
			}
		}
	}
}

func TestLineInformation(t *testing.T) {
	df, err := os.Open(testFileLineInfo)
	if err != nil {
		t.Fatalf("Dockerfile missing for %s: %v", testFileLineInfo, err)
	}
	defer df.Close()

	ast, _, err := Parse(df)
	if err != nil {
		t.Fatalf("Error parsing dockerfile %s: %v", testFileLineInfo, err)
	}

	if ast.startLine != 5 || ast.endLine != 31 {
		fmt.Fprintf(os.Stderr, "Wrong root line information: expected(%d-%d), actual(%d-%d)\n", 5, 31, ast.startLine, ast.endLine)
		t.Fatalf("Root line information doesn't match result.")
	}
	if len(ast.Children) != 3 {
		fmt.Fprintf(os.Stderr, "Wrong number of child: expected(%d), actual(%d)\n", 3, len(ast.Children))
		t.Fatalf("Root line information doesn't match result for %s", testFileLineInfo)
	}
	expected := [][]int{
		{5, 5},
		{11, 12},
		{17, 31},
	}
	for i, child := range ast.Children {
		if child.startLine != expected[i][0] || child.endLine != expected[i][1] {
			t.Logf("Wrong line information for child %d: expected(%d-%d), actual(%d-%d)\n",
				i, expected[i][0], expected[i][1], child.startLine, child.endLine)
			t.Fatalf("Root line information doesn't match result.")
		}
	}
}

func TestParseDirectives(t *testing.T) {
	dockerfile := `# net = host
# add-host = myhost,foo
from busybox`

	_, d, err := Parse(strings.NewReader(dockerfile))
	if err != nil {
		t.Fatalf("Dockerfile parsing failed %v", err)
	}
	if !d.NetMode.Matches("host") {
		t.Fatalf("Net directive host didn't match: %v", d.NetMode)
	}
	if d.NetMode.Matches("none") {
		t.Fatalf("Net directive none should not have matched: %v", d.NetMode)
	}
	if !d.ExtraHosts[0].Matches("myhost") {
		t.Fatalf("Host directive myhost should have matched: %v", d.ExtraHosts[0])
	}
	if !d.ExtraHosts[0].Matches("foo") {
		t.Fatalf("Host directive foo should have matched: %v", d.ExtraHosts[0])
	}
	if d.ExtraHosts[0].Matches("bar") {
		t.Fatalf("Host directive bar should not have matched: %v", d.ExtraHosts[0])
	}

	dockerfile = `# net = !none,!host
# add-host = foo
# add-host = !google.com
from busybox`

	_, d, err = Parse(strings.NewReader(dockerfile))
	if err != nil {
		t.Fatalf("Dockerfile parsing failed %v", err)
	}

	if !d.NetMode.Matches("bridge") {
		t.Fatalf("Net directive bridge didn't match: %v", d.NetMode)
	}
	if d.NetMode.Matches("none") {
		t.Fatalf("Net directive none should not have matched: %v", d.NetMode)
	}
	if !d.ExtraHosts[0].Matches("foo") {
		t.Fatalf("Host directive foo didn't match: %v", d.ExtraHosts[0])
	}
	if d.ExtraHosts[0].Matches("bar") {
		t.Fatalf("Host directive bar should not have matched: %v", d.ExtraHosts[0])
	}
	if d.ExtraHosts[1].Matches("google.com") {
		t.Fatalf("Host directive google.com should not have matched: %v", d.ExtraHosts[1])
	}
}
