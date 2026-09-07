package generate

import (
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/lestrrat-go/rasql/schema"
)

func snapshotDescription(in *querydescribe.Description) *querydescribe.Description {
	if in == nil {
		return nil
	}
	out := *in
	out.Columns = make([]querydescribe.Column, len(in.Columns))
	for i, column := range in.Columns {
		out.Columns[i] = column
		out.Columns[i].Binding.Imports = append([]schema.GoImport(nil), column.Binding.Imports...)
	}
	return &out
}

func snapshotDescriptionRequest(request querydescribe.Request) querydescribe.Request {
	out := request
	out.Parameters = append([]string(nil), request.Parameters...)
	out.Tables = make([]schema.TableDef, len(request.Tables))
	for i := range request.Tables {
		out.Tables[i] = request.Tables[i].Clone()
	}
	out.Expected = snapshotDescription(request.Expected)
	return out
}
