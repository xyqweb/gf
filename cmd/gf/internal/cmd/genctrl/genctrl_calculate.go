// Copyright GoFrame gf Author(https://goframe.org). All Rights Reserved.
//
// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT was not distributed with this file,
// You can obtain one at https://github.com/gogf/gf.

package genctrl

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"

	"github.com/gogf/gf/cmd/gf/v2/internal/utility/utils"
	"github.com/gogf/gf/v2/os/gfile"
	"github.com/gogf/gf/v2/text/gregex"
	"github.com/gogf/gf/v2/text/gstr"
)

func (c CGenCtrl) getApiItemsInSrc(apiModuleFolderPath string) (items []apiItem, err error) {
	var importPath string
	// The second level folders: versions.
	apiVersionFolderPaths, err := gfile.ScanDir(apiModuleFolderPath, "*", false)
	if err != nil {
		return nil, err
	}
	for _, apiVersionFolderPath := range apiVersionFolderPaths {
		if !gfile.IsDir(apiVersionFolderPath) {
			continue
		}
		// The second level folders: versions.
		apiFileFolderPaths, err := gfile.ScanDir(apiVersionFolderPath, "*.go", false)
		if err != nil {
			return nil, err
		}
		importPath = utils.GetImportPath(apiVersionFolderPath)
		for _, apiFileFolderPath := range apiFileFolderPaths {
			if gfile.IsDir(apiFileFolderPath) {
				continue
			}
			structsInfo, err := c.getStructsNameInSrc(apiFileFolderPath)
			if err != nil {
				return nil, err
			}
			for _, methodName := range structsInfo {
				// remove end "Req"
				methodName = gstr.TrimRightStr(methodName, "Req", 1)
				item := apiItem{
					Import:     gstr.Trim(importPath, `"`),
					FileName:   gfile.Name(apiFileFolderPath),
					Module:     gfile.Basename(apiModuleFolderPath),
					Version:    gfile.Basename(apiVersionFolderPath),
					MethodName: methodName,
				}
				items = append(items, item)
			}
		}
	}
	return
}

func (c CGenCtrl) getApiItemsInDst(dstFolder string) (items []apiItem, err error) {
	if !gfile.Exists(dstFolder) {
		return nil, nil
	}
	type importItem struct {
		Path  string
		Alias string
	}
	filePaths, err := gfile.ScanDir(dstFolder, "*.go", true)
	if err != nil {
		return nil, err
	}
	for _, filePath := range filePaths {
		var (
			array       []string
			importItems []importItem
			importLines []string
			module      = gfile.Basename(gfile.Dir(filePath))
		)
		importLines, err = c.getImportsInDst(filePath)
		if err != nil {
			return nil, err
		}

		// retrieve all imports.
		for _, importLine := range importLines {
			array = gstr.SplitAndTrim(importLine, " ")
			if len(array) == 2 {
				importItems = append(importItems, importItem{
					Path:  gstr.Trim(array[1], `"`),
					Alias: array[0],
				})
			} else {
				importItems = append(importItems, importItem{
					Path: gstr.Trim(array[0], `"`),
				})
			}
		}
		// retrieve all api usages.
		// retrieve it without using AST, but use regular expressions to retrieve.
		// It's because the api definition is simple and regular.
		// Use regular expressions to get better performance.
		fileContent := gfile.GetContents(filePath)
		matches, err := gregex.MatchAllString(PatternCtrlDefinition, fileContent)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			// try to find the import path of the api.
			var (
				importPath string
				version    = match[1]
				methodName = match[2] // not the function name, but the method name in api definition.
			)
			for _, item := range importItems {
				if item.Alias != "" {
					if item.Alias == version {
						importPath = item.Path
						break
					}
					continue
				}
				if gfile.Basename(item.Path) == version {
					importPath = item.Path
					break
				}
			}
			item := apiItem{
				Import:     gstr.Trim(importPath, `"`),
				Module:     module,
				Version:    gfile.Basename(importPath),
				MethodName: methodName,
			}
			items = append(items, item)
		}
	}
	return
}

// getStructsNameInSrc retrieves all struct names
// that end in "Req" and have "g.Meta" in their body.
func (c CGenCtrl) getStructsNameInSrc(filePath string) (structsName []string, err error) {
	var (
		fileContent = gfile.GetContents(filePath)
		fileSet     = token.NewFileSet()
	)

	node, err := parser.ParseFile(fileSet, "", fileContent, parser.ParseComments)
	if err != nil {
		return
	}

	ast.Inspect(node, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok {
			methodName := typeSpec.Name.Name
			if !gstr.HasSuffix(methodName, "Req") {
				// ignore struct name that do not end in "Req"
				return true
			}
			if structType, ok := typeSpec.Type.(*ast.StructType); ok {
				var buf bytes.Buffer
				if err := printer.Fprint(&buf, fileSet, structType); err != nil {
					return false
				}
				// ignore struct name that match a request, but has no g.Meta in its body.
				if !gstr.Contains(buf.String(), `g.Meta`) {
					return true
				}
				structsName = append(structsName, methodName)
			}
		}
		return true
	})

	return
}

// getImportsInDst retrieves all import paths in the file.
func (c CGenCtrl) getImportsInDst(filePath string) (imports []string, err error) {
	var (
		fileContent = gfile.GetContents(filePath)
		fileSet     = token.NewFileSet()
	)

	node, err := parser.ParseFile(fileSet, "", fileContent, parser.ParseComments)
	if err != nil {
		return
	}

	ast.Inspect(node, func(n ast.Node) bool {
		if imp, ok := n.(*ast.ImportSpec); ok {
			imports = append(imports, imp.Path.Value)
		}
		return true
	})

	return
}
