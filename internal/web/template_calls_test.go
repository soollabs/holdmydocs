package web

import (
	"testing"
	"text/template/parse"
)

// dynamicSample stands in for a non-literal option value so the option name
// is still validated. Keys absent here get a generic non-empty value.
var dynamicSample = map[string]string{
	"pressed": "true", "disabled": "false", "hidden": "false", "formnovalidate": "false", "new-tab": "false",
	"checked": "false", "open": "false", "required": "false", "multiple": "false", "aria-expanded": "false",
}

func dynamicValue(key string) string {
	if v, ok := dynamicSample[key]; ok {
		return v
	}
	return "x"
}

// TestTemplateComponentCallsAreValid checks every button/component call in
// every template, including branches a render with sample data would skip, so
// a mistyped option fails here rather than when someone opens the page.
func TestTemplateComponentCallsAreValid(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for page, tmpl := range templates {
		for _, def := range tmpl.Templates() {
			if def.Tree == nil {
				continue
			}
			walkTemplateNode(def.Tree.Root, func(cmd *parse.CommandNode) {
				name, ok := cmd.Args[0].(*parse.IdentifierNode)
				if !ok || (name.Ident != "button" && name.Ident != "component") {
					return
				}
				calls++
				args := make([]string, 0, len(cmd.Args)-1)
				for i, arg := range cmd.Args[1:] {
					if s, ok := arg.(*parse.StringNode); ok {
						args = append(args, s.Text)
						continue
					}
					// Labels and option values may be dynamic; names and kinds may not.
					switch {
					case name.Ident == "button" && i == 0:
						args = append(args, "label")
					case name.Ident == "button" && i >= 2 && i%2 == 1:
						args = append(args, dynamicValue(args[len(args)-1]))
					case name.Ident == "component" && i >= 1 && i%2 == 0:
						args = append(args, dynamicValue(args[len(args)-1]))
					default:
						t.Errorf("%s/%s: %s argument %d must be a string literal: %s", page, def.Name(), name.Ident, i, cmd)
						return
					}
				}
				if name.Ident == "button" {
					if len(args) < 2 {
						t.Errorf("%s/%s: %s", page, def.Name(), cmd)
						return
					}
					_, err = makeButton(args[0], args[1], args[2:]...)
				} else {
					_, err = makeComponent(args[0], args[1:]...)
				}
				if err != nil {
					t.Errorf("%s/%s: %s: %v", page, def.Name(), cmd, err)
				}
			})
		}
	}
	if calls == 0 {
		t.Fatal("found no component calls; walker is broken")
	}
}

func walkTemplateNode(node parse.Node, visit func(*parse.CommandNode)) {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, child := range n.Nodes {
			walkTemplateNode(child, visit)
		}
	case *parse.ActionNode:
		walkTemplateNode(n.Pipe, visit)
	case *parse.TemplateNode:
		walkTemplateNode(n.Pipe, visit)
	case *parse.IfNode:
		walkTemplateNode(&n.BranchNode, visit)
	case *parse.RangeNode:
		walkTemplateNode(&n.BranchNode, visit)
	case *parse.WithNode:
		walkTemplateNode(&n.BranchNode, visit)
	case *parse.BranchNode:
		walkTemplateNode(n.Pipe, visit)
		walkTemplateNode(n.List, visit)
		walkTemplateNode(n.ElseList, visit)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		for _, cmd := range n.Cmds {
			walkTemplateNode(cmd, visit)
		}
	case *parse.CommandNode:
		visit(n)
		for _, arg := range n.Args {
			walkTemplateNode(arg, visit)
		}
	}
}
