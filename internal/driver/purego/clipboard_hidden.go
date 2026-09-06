//go:build purego && !linux

package purego

func hiddenClipboard() clipboardSurface { return nil }

func closeHiddenClipboard() {}
