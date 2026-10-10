package pg

import "strings"

// RedactSecrets uses the resolved pool credential, including helper, keyring,
// pgpass and pgx discovery sources, without exposing it to the UI or logger.
func (c *Connection) RedactSecrets(text string) string {
	if c == nil || c.pool == nil {
		return text
	}
	password := c.pool.Config().ConnConfig.Password
	if password == "" {
		return text
	}
	encoded := strings.NewReplacer("'", "''", "\\", "\\\\").Replace(password)
	text = strings.ReplaceAll(text, encoded, "***")
	text = strings.ReplaceAll(text, strings.ReplaceAll(password, "'", "''"), "***")
	return strings.ReplaceAll(text, password, "***")
}
