package models

// FunctionDetail describes a stored function returned by
// Session.DescribeFunction. See DESIGN.md §11.1.
type FunctionDetail struct {
	Schema     string
	Name       string
	Args       []FunctionArg
	ReturnType string
	Volatility string
	Language   string
}

// FunctionArg is a single argument of a FunctionDetail.
type FunctionArg struct {
	Name string
	Type string
	Mode string
}
