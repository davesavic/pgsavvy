// Package drivers defines the storage-engine-independent driver contract
// (Driver, Connection, Session, RowStream, Transaction) along with the
// Capabilities shape, the Factory registry, and the shared error sentinels
// consumed by every concrete driver under pkg/drivers/*. See DESIGN.md §11.
package drivers
