// Copyright (c) 2013, Vastech SA (PTY) LTD. All rights reserved.
// http://code.google.com/p/gogoprotobuf/gogoproto
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//     * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//     * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package generator

import (
	"bytes"
	"go/parser"
	"go/printer"
	"go/token"
	"strconv"
	"strings"

	"code.google.com/p/gogoprotobuf/gogoproto"
	"code.google.com/p/gogoprotobuf/proto"
	descriptor "code.google.com/p/gogoprotobuf/protoc-gen-gogo/descriptor"
	plugin "code.google.com/p/gogoprotobuf/protoc-gen-gogo/plugin"
	"errors"
	"path"
)

func (d *FileDescriptor) Messages() []*Descriptor {
	return d.desc
}

func (d *FileDescriptor) Enums() []*EnumDescriptor {
	return d.enum
}

func (d *Descriptor) IsGroup() bool {
	return d.group
}

func (g *Generator) IsGroup(field *descriptor.FieldDescriptorProto) bool {
	if d, ok := g.typeNameToObject[field.GetTypeName()].(*Descriptor); ok {
		return d.IsGroup()
	}
	return false
}

func (g *Generator) TypeNameByObject(typeName string) Object {
	o, ok := g.typeNameToObject[typeName]
	if !ok {
		g.Fail("can't find object with type", typeName)
	}
	return o
}

type PluginImports interface {
	NewImport(pkg string) Single
	GenerateImports(file *FileDescriptor)
}

type pluginImports struct {
	generator *Generator
	singles   []Single
}

func NewPluginImports(generator *Generator) *pluginImports {
	return &pluginImports{generator, make([]Single, 0)}
}

func (this *pluginImports) NewImport(pkg string) Single {
	imp := newImportedPackage(this.generator.ImportPrefix, pkg)
	this.singles = append(this.singles, imp)
	return imp
}

func (this *pluginImports) GenerateImports(file *FileDescriptor) {
	for _, s := range this.singles {
		if s.IsUsed() {
			this.generator.P(s.Generate())
		}
	}
}

type Single interface {
	Use() string
	IsUsed() bool
	Generate() string
}

type importedPackage struct {
	used         bool
	pkg          string
	name         string
	importPrefix string
}

func newImportedPackage(importPrefix, pkg string) *importedPackage {
	return &importedPackage{
		pkg:          pkg,
		importPrefix: importPrefix,
	}
}

func (this *importedPackage) Use() string {
	if !this.used {
		this.name = RegisterUniquePackageName(this.pkg, nil)
		this.used = true
	}
	return this.name
}

func (this *importedPackage) IsUsed() bool {
	return this.used
}

func (this *importedPackage) Generate() string {
	return strings.Join([]string{`import `, this.name, ` `, strconv.Quote(this.importPrefix + this.pkg)}, "")
}

func GoTypeToName(goTyp string) string {
	return strings.Replace(strings.Replace(goTyp, "*", "", -1), "[]", "", -1)
}

func EmbedFieldName(goTyp string) string {
	goTyp = GoTypeToName(goTyp)
	goTyps := strings.Split(goTyp, ".")
	if len(goTyps) == 1 {
		return goTyp
	}
	if len(goTyps) == 2 {
		return goTyps[1]
	}
	panic("unreachable")
}

func (g *Generator) GeneratePlugin(p Plugin) {
	p.Init(g)
	// Generate the output. The generator runs for every file, even the files
	// that we don't generate output for, so that we can collate the full list
	// of exported symbols to support public imports.
	genFileMap := make(map[*FileDescriptor]bool, len(g.genFiles))
	for _, file := range g.genFiles {
		genFileMap[file] = true
	}
	i := 0
	for _, file := range g.allFiles {
		g.Reset()
		g.generatePlugin(file, p)
		if _, ok := genFileMap[file]; !ok {
			continue
		}
		g.Response.File[i] = new(plugin.CodeGeneratorResponse_File)
		g.Response.File[i].Name = proto.String(goFileName(*file.Name))
		g.Response.File[i].Content = proto.String(g.String())
		i++
	}
}

func (g *Generator) generatePlugin(file *FileDescriptor, p Plugin) {
	g.file = g.FileOf(file.FileDescriptorProto)
	g.usedPackages = make(map[string]bool)

	// Run the plugins before the imports so we know which imports are necessary.
	p.Generate(file)

	// Generate header and imports last, though they appear first in the output.
	rem := g.Buffer
	g.Buffer = new(bytes.Buffer)
	g.generateHeader()
	p.GenerateImports(g.file)
	g.Write(rem.Bytes())

	// Reformat generated code.
	fset := token.NewFileSet()
	ast, err := parser.ParseFile(fset, "", g, parser.ParseComments)
	if err != nil {
		g.Fail("bad Go source code was generated:", err.Error())
		return
	}
	g.Reset()
	err = (&printer.Config{Mode: printer.TabIndent | printer.UseSpaces, Tabwidth: 8}).Fprint(g, fset, ast)
	if err != nil {
		g.Fail("generated Go source code could not be reformatted:", err.Error())
	}
}

func GetCustomType(field *descriptor.FieldDescriptorProto) (packageName string, typ string, err error) {
	return getCustomType(field)
}

func getCustomType(field *descriptor.FieldDescriptorProto) (packageName string, typ string, err error) {
	if field.Options != nil {
		v, err := proto.GetExtension(field.Options, gogoproto.E_Customtype)
		if err == nil && v.(*string) != nil {
			ctype := *(v.(*string))
			ss := strings.Split(ctype, ".")
			if len(ss) == 1 {
				return "", typ, nil
			} else if len(ss) >= 2 {
				packageName := strings.Join(ss[0:len(ss)-1], ".")
				typeName := ss[len(ss)-1]
				importStr := strings.Replace(strings.Replace(packageName, "/", "_", -1), ".", "_", -1)
				typ = importStr + "." + typeName
				return typ, packageName, nil
			} else {
				err = errors.New("custom type requires a dot-separated import and type")
			}
		}
	}
	return "", "", err
}

func FileName(file *FileDescriptor) string {
	return CamelCase(strings.Replace(path.Base(file.FileDescriptorProto.GetName()), ".proto", "", -1))
}

func (g *Generator) AllFiles() *descriptor.FileDescriptorSet {
	set := &descriptor.FileDescriptorSet{}
	set.File = make([]*descriptor.FileDescriptorProto, len(g.allFiles))
	for i := range g.allFiles {
		set.File[i] = g.allFiles[i].FileDescriptorProto
	}
	return set
}
