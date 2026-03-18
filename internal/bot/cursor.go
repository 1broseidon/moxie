package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/1broseidon/moxie/internal/store"
)

func cursorFile() string {
	return filepath.Join(store.ConfigDir(), "telegram-cursor")
}

func legacyCursorFile() string {
	return store.ConfigFile("cursor")
}

func CursorOffset() int {
	if c := ReadCursor(); c > 0 {
		return c + 1
	}
	return 0
}

func ReadCursor() int {
	for _, path := range []string{cursorFile(), legacyCursorFile()} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

func WriteCursor(id int) {
	_ = os.WriteFile(cursorFile(), []byte(fmt.Sprintf("%d", id)), 0o600)
}
