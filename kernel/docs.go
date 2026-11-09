package kernel

import (
	"reflect"
	"runtime"
	"strings"
)

type OperationDoc struct {
	Summary     string
	Description string
}

// DocSet is produced by cmd/plinth-docgen. Operation keys are fully-qualified
// Go function names; field keys are <pkg>.<Type>.<Field>. Nothing is read from
// source files at runtime.
type DocSet struct {
	Operations map[string]OperationDoc
	Fields     map[string]string
}

func (r *Router) SetDocs(docs DocSet) {
	if r.frozen {
		panic("kernel: Router.SetDocs must be called before registering routes")
	}
	r.docs = docs
}

func (r *Router) operationDoc(handler any) (OperationDoc, bool) {
	name := handlerName(handler)
	if name == "" {
		return OperationDoc{}, false
	}
	doc, ok := r.docs.Operations[name]
	return doc, ok
}

func handlerName(handler any) string {
	if handler == nil {
		return ""
	}
	v := reflect.ValueOf(handler)
	if v.Kind() != reflect.Func || v.IsNil() {
		return ""
	}
	fn := runtime.FuncForPC(v.Pointer())
	if fn == nil {
		return ""
	}
	name := strings.TrimSuffix(fn.Name(), "-fm")
	name = strings.ReplaceAll(name, ".(*", ".")
	name = strings.ReplaceAll(name, ").", ".")
	return name
}

func fieldDocKey(t reflect.Type, name string) string {
	t = indirectType(t)
	if t.PkgPath() == "" || t.Name() == "" {
		return ""
	}
	return t.PkgPath() + "." + t.Name() + "." + name
}
