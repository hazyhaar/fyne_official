//go:build purego

package purego

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/internal/driver/menu"
	"fyne.io/fyne/v2/lang"
)

func buildMenuOverlay(menus *fyne.MainMenu, w *window) fyne.CanvasObject {
	if len(menus.Items) == 0 {
		fyne.LogError("Main menu must have at least one child menu", nil)
		return nil
	}

	menus = addMissingQuitForMainMenu(menus, w)
	return menu.NewMenuBar(menus, w.canvas)
}

func (c *canvas) ToggleMenu() {
	if c.menu == nil {
		return
	}
	if bar, ok := c.menu.(*menu.MenuBar); ok {
		bar.Toggle()
	}
}

func (c *canvas) DismissMenu() bool {
	if c.menu == nil {
		return false
	}
	bar, ok := c.menu.(*menu.MenuBar)
	if !ok || !bar.IsActive() {
		return false
	}
	bar.Toggle()
	return true
}

func addMissingQuitForMainMenu(menus *fyne.MainMenu, w *window) *fyne.MainMenu {
	localQuit := lang.L("Quit")
	var lastItem *fyne.MenuItem
	if len(menus.Items[0].Items) > 0 {
		lastItem = menus.Items[0].Items[len(menus.Items[0].Items)-1]
		if lastItem.Label == localQuit {
			lastItem.IsQuit = true
		}
	}
	if lastItem == nil || !lastItem.IsQuit { // make sure the first menu always has a quit option
		quitItem := fyne.NewMenuItem(localQuit, nil)
		quitItem.IsQuit = true
		menus.Items[0].Items = append(menus.Items[0].Items, fyne.NewMenuItemSeparator(), quitItem)
	}
	for _, item := range menus.Items[0].Items {
		if item.IsQuit && item.Action == nil {
			item.Action = func() {
				if w.driver == nil {
					w.Close()
					return
				}
				for _, win := range w.driver.AllWindows() {
					win.Close()
				}
			}
		}
	}
	return menus
}

func (c *canvas) clearMenuShortcuts() {
	for _, s := range c.menuShortcuts {
		c.RemoveShortcut(s)
	}
	c.menuShortcuts = nil
}

func (c *canvas) registerMenuShortcuts(menus *fyne.MainMenu) {
	if menus == nil {
		return
	}
	var walk func(*fyne.Menu)
	walk = func(m *fyne.Menu) {
		if m == nil {
			return
		}
		for _, item := range m.Items {
			if item.Shortcut != nil && item.Action != nil {
				sc := item.Shortcut
				act := item.Action
				c.AddShortcut(sc, func(fyne.Shortcut) { act() })
				c.menuShortcuts = append(c.menuShortcuts, sc)
			}
			if item.ChildMenu != nil {
				walk(item.ChildMenu)
			}
		}
	}
	for _, m := range menus.Items {
		walk(m)
	}
}
