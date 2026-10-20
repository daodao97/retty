package agenthistory

// Project history is a desktop feature. Do not link the desktop SQLite plugin
// or package its native libraries into the phone app.
func readCodexDatabase(string) ([]Session, error) { return nil, nil }
