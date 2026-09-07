package querydescribe

import "github.com/lestrrat-go/rasql/internal/compilerquery"

type SQLClassification = compilerquery.Classification

func ClassifySQL(sqlText string) (SQLClassification, error) {
	return compilerquery.ClassifySQL(sqlText)
}
