// Copyright (c) Contributors to the Apptainer project, established as
//   Apptainer a Series of LF Projects LLC.
//   For website terms of use, trademark policy, privacy policy and other
//   project policies see https://lfprojects.org/policies
// Copyright (c) 2018-2022, Sylabs Inc. All rights reserved.
// This software is licensed under a 3-clause BSD license. Please consult the
// LICENSE.md file distributed with the sources of this project regarding your
// rights to use or distribute this software.

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/apptainer/apptainer/pkg/sylog"
)

func parseLine(s string) (d Define) {
	d = Define{
		Words: strings.Fields(s),
	}

	return
}

// Define is a struct that contains one line of configuration words.
type Define struct {
	Words []string
}

// WriteLine writes a line of configuration.
func (d Define) WriteLine() (s string) {
	s = d.Words[2]
	if len(d.Words) > 3 {
		for _, w := range d.Words[3:] {
			s += " + " + w
		}
	}

	varType := "const"
	varStatement := d.Words[1] + " = " + s

	// Apply runtime relocation to some variables
	switch d.Words[1] {
	case
		"BINDIR",
		"LIBEXECDIR",
		"SYSCONFDIR",
		"SESSIONDIR",
		"APPTAINER_CONFDIR",
		"PLUGIN_ROOTDIR":
		varType = "var"
		varStatement = d.Words[1] + " = relocatePath(" + s + ")"
	case "APPTAINER_SUID_INSTALL":
		varType = "var"
		varStatement = d.Words[1] + " = isSuidInstall()"
	default:
		if strings.Contains(s, "APPTAINER_CONFDIR") {
			varType = "var"
		}
	}

	return varType + " " + varStatement
}

var confgenTemplate = template.Must(template.New("").Parse(`// Code generated by go generate; DO NOT EDIT.
package buildcfg

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/apptainer/apptainer/pkg/sylog"
)

var (
	prefixOnce    sync.Once
	installPrefix string
	isSuidOnce    sync.Once
	suidInstall   int
)

func getPrefix() (string) {
	prefixOnce.Do(func() {
		executablePath, err := os.Executable()
		if err != nil {
			sylog.Warningf("Error getting executable path, using default: %v", err)
			installPrefix = "{{.Prefix}}"
			return
		}

		bin := filepath.Dir(executablePath)
		base := filepath.Base(executablePath)

		switch base {
		case "apptainer":
			// PREFIX/bin/apptainer
			installPrefix = filepath.Dir(bin)
		case "starter", "starter-suid":
			// PREFIX/libexec/apptainer/bin/starter{|-suid}
			installPrefix = filepath.Dir(filepath.Dir(filepath.Dir(bin)))
		default:
			// don't relocate unknown base
			installPrefix = "{{.Prefix}}"
		}
		sylog.Debugf("Install prefix is %s", installPrefix)
	})
	return installPrefix
}

// This needs to be a Once to avoid a possible race condition attack.
// Otherwise it is possible to let it fail to find the starter-suid the first
// attempt and then slip in a symlink to a setuid starter-suid elsewhere,
// and fool it into using an attacker-controlled configuration file.
func isSuidInstall() int {
	isSuidOnce.Do(func() {
		prefix := getPrefix()
		path := prefix + "/libexec/apptainer/bin/starter-suid"
		_, err := os.Stat(path)
		if err == nil {
			suidInstall = 1
		}
	})
	return suidInstall
}

func relocatePath(original string) string {
	if "{{.Prefix}}" == "" || "{{.Prefix}}" == "/" {
		return original
	}
	rootPrefix := false
	if !strings.HasPrefix(original, "{{.Prefix}}") {
		if strings.HasPrefix(original, "/etc/apptainer") ||
			strings.HasPrefix(original, "/var/apptainer") {
			// These are typically the only pieces not under
			// "/usr" (which is the prefix) in packages
			rootPrefix = true
		} else {
			return original
		}
	}

	prefix := getPrefix()
	if prefix == "{{.Prefix}}" {
		return original
	}

	if isSuidInstall() == 1 {
		// For security reasons, do not relocate when there
		// is a starter-suid
		sylog.Fatalf("Relocation not allowed with starter-suid")
	}

	var relativePath string
	var err error
	if rootPrefix {
		relativePath, err = filepath.Rel("/", original)
	} else {
		relativePath, err = filepath.Rel("{{.Prefix}}", original)
	}
	if err != nil {
		sylog.Fatalf(err.Error())
	}

	result := filepath.Join(prefix, relativePath)
	return result
}

{{ range $i, $d := .Defines }}
{{$d.WriteLine -}}
{{end}}
`))

func main() {
	outFile, err := os.Create("config.go")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer outFile.Close()

	// Parse the config.h file
	inFile, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println(err)
		return
	}

	header := []Define{}
	s := bufio.NewScanner(bytes.NewReader(inFile))
	prefix := ""
	for s.Scan() {
		d := parseLine(s.Text())
		if len(d.Words) > 2 && d.Words[0] == "#define" {
			if d.Words[1] == "PREFIX" {
				if len(d.Words) != 3 {
					sylog.Fatalf("Expected PREFIX to contain 3 elements")
				}
				prefix = d.Words[2]
			}
			header = append(header, d)
		}
	}
	if prefix == "" {
		sylog.Fatalf("Failed to find value of PREFIX")
	}

	if goBuildTags := os.Getenv("GO_BUILD_TAGS"); goBuildTags != "" {
		d := Define{
			Words: []string{
				"#define",
				"GO_BUILD_TAGS",
				fmt.Sprintf("`%s`", goBuildTags),
			},
		}
		header = append(header, d)
	}

	data := struct {
		Prefix  string
		Defines []Define
	}{
		prefix[1 : len(prefix)-1],
		header,
	}
	err = confgenTemplate.Execute(outFile, data)
	if err != nil {
		fmt.Println(err)
		return
	}
}
