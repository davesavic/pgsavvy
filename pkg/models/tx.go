package models

// TxOptions configures a transaction opened via Session.Begin. See DESIGN.md
// §11.1.
type TxOptions struct {
	IsoLevel   string
	ReadOnly   bool
	Deferrable bool
}

// TxStatus is the lifecycle status of a transaction. See DESIGN.md §11.1
// (Transaction.Status()).
type TxStatus string

const (
	TxActive      TxStatus = "active"
	TxCommitted   TxStatus = "committed"
	TxRolledBack  TxStatus = "rolled_back"
	TxAbortedInTx TxStatus = "aborted_in_tx"
)
