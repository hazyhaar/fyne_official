//go:build purego

package purego

import (
	"image"
	"image/draw"
	"sync/atomic"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/internal"
	"fyne.io/fyne/v2/internal/app"
	"fyne.io/fyne/v2/internal/async"
	"fyne.io/fyne/v2/internal/cache"
	intdriver "fyne.io/fyne/v2/internal/driver"
	"fyne.io/fyne/v2/internal/painter/software"
	"fyne.io/fyne/v2/internal/scale"
	inttheme "fyne.io/fyne/v2/internal/theme"
	"fyne.io/fyne/v2/theme"
)

var (
	_ fyne.Canvas    = (*canvas)(nil)
	_ desktop.Canvas = (*canvas)(nil)
)

type canvas struct {
	fyne.ShortcutHandler

	content         fyne.CanvasObject
	menu            fyne.CanvasObject
	menuShortcuts   []fyne.Shortcut
	overlays        internal.OverlayStack
	contentFocusMgr *app.FocusManager
	menuFocusMgr    *app.FocusManager
	minSizes        map[fyne.CanvasObject]fyne.Size
	padded          bool
	size            fyne.Size
	scale           float32
	dirty           atomic.Bool
	refreshQueue    *async.CanvasObjectQueue

	onTypedRune func(rune)
	onTypedKey  func(*fyne.KeyEvent)
	onKeyDown   func(*fyne.KeyEvent)
	onKeyUp     func(*fyne.KeyEvent)

	rasterizer *software.Painter

	frame      *image.NRGBA
	dirtyR     image.Rectangle
	dirtyAll   atomic.Bool
	lastBounds map[fyne.CanvasObject]image.Rectangle
	menuRoot   *fyne.Container

	onDirty func()
}

func newCanvas() *canvas {
	c := &canvas{
		padded:          true,
		scale:           1,
		rasterizer:      software.NewPainter(),
		contentFocusMgr: app.NewFocusManager(nil),
		refreshQueue:    async.NewCanvasObjectQueue(),
	}
	c.overlays.Canvas = c
	c.overlays.OnChange = c.overlaysChanged
	return c
}

func (c *canvas) Capture() image.Image {
	size := c.Size()
	bounds := image.Rect(0, 0, scale.ToScreenCoordinate(c, size.Width), scale.ToScreenCoordinate(c, size.Height))
	img := image.NewNRGBA(bounds)
	draw.Draw(img, bounds, image.NewUniform(theme.Color(theme.ColorNameBackground)), image.Point{}, draw.Src)
	draw.Draw(img, bounds, c.rasterizer.Paint(c.paintTarget()), image.Point{}, draw.Over)
	return img
}

func (c *canvas) Content() fyne.CanvasObject { return c.content }

func (c *canvas) SetContent(content fyne.CanvasObject) {
	old := c.content
	c.content = content
	c.dropObjectCache(old)
	newFocusMgr := app.NewFocusManager(content)
	if c.contentFocusMgr != nil {
		if focused := c.contentFocusMgr.Focused(); focused != nil {
			newFocusMgr.Focus(focused)
		}
	}
	c.contentFocusMgr = newFocusMgr
	if content != nil {
		if c.size.IsZero() {
			content.Resize(content.MinSize())
		} else {
			content.Resize(c.contentSize(c.size))
			content.Move(c.contentPos())
		}
	}
	c.SetDirty()
}

func (c *canvas) InteractiveArea() (fyne.Position, fyne.Size) {
	return fyne.NewPos(0, 0), c.size
}

func (c *canvas) MinSize() fyne.Size {
	minSize := fyne.NewSize(0, c.menuHeight())
	if c.content != nil {
		minSize = c.content.MinSize().Add(fyne.NewSize(0, c.menuHeight()))
	}
	if c.padded {
		minSize = minSize.Add(fyne.NewSquareSize(theme.Padding() * 2))
	}
	return minSize
}

func (c *canvas) OnTypedKey() func(*fyne.KeyEvent)     { return c.onTypedKey }
func (c *canvas) OnTypedRune() func(rune)              { return c.onTypedRune }
func (c *canvas) SetOnTypedKey(f func(*fyne.KeyEvent)) { c.onTypedKey = f }
func (c *canvas) SetOnTypedRune(f func(rune))          { c.onTypedRune = f }
func (c *canvas) OnKeyDown() func(*fyne.KeyEvent)      { return c.onKeyDown }
func (c *canvas) OnKeyUp() func(*fyne.KeyEvent)        { return c.onKeyUp }
func (c *canvas) SetOnKeyDown(f func(*fyne.KeyEvent))  { c.onKeyDown = f }
func (c *canvas) SetOnKeyUp(f func(*fyne.KeyEvent))    { c.onKeyUp = f }

func (c *canvas) PixelCoordinateForPosition(pos fyne.Position) (x, y int) {
	return scale.ToScreenCoordinate(c, pos.X), scale.ToScreenCoordinate(c, pos.Y)
}

func (c *canvas) Resize(size fyne.Size) {
	c.size = size
	if c.content != nil {
		c.content.Move(c.contentPos())
		c.content.Resize(c.contentSize(size))
	}
	if c.menu != nil {
		c.menu.Refresh()
		c.menu.Resize(fyne.NewSize(size.Width, c.menuHeight()))
	}
	for _, overlay := range c.overlays.List() {
		overlay.Resize(size)
	}
	c.SetDirty()
}

func (c *canvas) Scale() float32 { return c.scale }

func (c *canvas) Size() fyne.Size { return c.size }

func (c *canvas) Overlays() fyne.OverlayStack { return &c.overlays }

func (c *canvas) overlaysChanged() {
	c.pruneObjectCaches()
	c.SetDirty()
}

func (c *canvas) SetPadded(padded bool) {
	c.padded = padded
	if c.content != nil {
		c.content.Move(c.contentPos())
		if !c.size.IsZero() {
			c.content.Resize(c.contentSize(c.size))
		}
	}
	c.SetDirty()
}

func (c *canvas) Focus(obj fyne.Focusable) {
	focusMgr := c.focusManager()
	if focusMgr != nil && focusMgr.Focus(obj) {
		return
	}
	managers := append([]*app.FocusManager{c.contentFocusMgr, c.menuFocusMgr}, c.overlays.ListFocusManagers()...)
	for _, mgr := range managers {
		if mgr == nil {
			continue
		}
		if focusMgr != mgr && mgr.Focus(obj) {
			return
		}
	}
	fyne.LogError("Failed to focus object which is not part of the canvas’ content, menu or overlays.", nil)
}

func (c *canvas) Focused() fyne.Focusable {
	mgr := c.focusManager()
	if mgr == nil {
		return nil
	}
	return mgr.Focused()
}

// FocusGained and FocusLost forward window-level focus to the focus manager,
// so the focused object sees FocusGained/FocusLost when the window does.
func (c *canvas) FocusGained() {
	if mgr := c.focusManager(); mgr != nil {
		mgr.FocusGained()
	}
}

func (c *canvas) FocusLost() {
	if mgr := c.focusManager(); mgr != nil {
		mgr.FocusLost()
	}
}

func (c *canvas) FocusNext() {
	if mgr := c.focusManager(); mgr != nil {
		mgr.FocusNext()
	}
}

func (c *canvas) FocusPrevious() {
	if mgr := c.focusManager(); mgr != nil {
		mgr.FocusPrevious()
	}
}

func (c *canvas) Unfocus() {
	if mgr := c.focusManager(); mgr != nil {
		mgr.Focus(nil)
	}
}

func (c *canvas) focusManager() *app.FocusManager {
	if focusMgr := c.overlays.TopFocusManager(); focusMgr != nil {
		return focusMgr
	}
	if c.isMenuActive() {
		return c.menuFocusMgr
	}
	return c.contentFocusMgr
}

func (c *canvas) isMenuActive() bool {
	if c.menu == nil {
		return false
	}
	if am, ok := c.menu.(interface{ IsActive() bool }); ok {
		return am.IsActive()
	}
	return true
}

func (c *canvas) ObjectTrees() []fyne.CanvasObject {
	trees := make([]fyne.CanvasObject, 0, len(c.overlays.List())+2)
	trees = append(trees, c.content)
	if c.menu != nil {
		trees = append(trees, c.menu)
	}
	trees = append(trees, c.overlays.List()...)
	return trees
}

func (c *canvas) EnsureMinSize() bool {
	c.drainRefreshQueue()
	if c.content == nil {
		return false
	}
	windowNeedsMinSizeUpdate := false
	csize := c.Size()
	minSize := c.MinSize()
	for _, tree := range c.ObjectTrees() {
		if tree == nil {
			continue
		}
		if c.ensureMinSizeTree(tree, csize) {
			windowNeedsMinSizeUpdate = true
		}
	}
	if windowNeedsMinSizeUpdate && (csize.Width < minSize.Width || csize.Height < minSize.Height) {
		c.Resize(internal.MaxSizes(csize, minSize))
	}
	return windowNeedsMinSizeUpdate
}

func (c *canvas) ensureMinSizeTree(root fyne.CanvasObject, csize fyne.Size) bool {
	windowNeeds := false
	var parentNeedingUpdate fyne.CanvasObject
	intdriver.WalkVisibleObjectTree(root,
		func(obj fyne.CanvasObject, _, _ fyne.Position, _ fyne.Size) bool {
			if th, ok := obj.(*container.ThemeOverride); ok {
				inttheme.PushRenderingTheme(th.Theme)
			}
			return false
		},
		func(obj fyne.CanvasObject, _ fyne.Position, parent fyne.CanvasObject) {
			cache.SetCanvasForObject(obj, c, func() {
				if img, ok := obj.(*fynecanvas.Image); ok {
					img.Refresh()
				}
			})
			if parentNeedingUpdate == obj {
				c.updateLayout(obj)
				parentNeedingUpdate = nil
			}
			if !obj.Visible() {
				c.popThemeOverride(obj)
				return
			}
			minSize := obj.MinSize()
			prev, ok := c.minSizes[obj]
			if !ok || prev != minSize {
				if c.minSizes == nil {
					c.minSizes = make(map[fyne.CanvasObject]fyne.Size)
				}
				c.minSizes[obj] = minSize
				if parent != nil {
					parentNeedingUpdate = parent
				} else {
					windowNeeds = true
					size := obj.Size()
					expected := internal.MaxSizes(minSize, size)
					if expected != size && size != csize {
						obj.Resize(expected)
					} else {
						c.updateLayout(obj)
					}
				}
			}
			c.popThemeOverride(obj)
		},
	)
	return windowNeeds
}

func (*canvas) popThemeOverride(obj fyne.CanvasObject) {
	if _, ok := obj.(*container.ThemeOverride); ok {
		inttheme.PopRenderingTheme()
	}
}

func (*canvas) updateLayout(obj fyne.CanvasObject) {
	switch cont := obj.(type) {
	case *fyne.Container:
		if cont.Layout != nil {
			cont.Layout.Layout(cont.Objects, cont.Size())
		}
	case fyne.Widget:
		cache.Renderer(cont).Layout(cont.Size())
	}
}

func (c *canvas) CheckDirtyAndClear() bool {
	c.drainRefreshQueue()
	return c.dirty.Swap(false)
}

func (c *canvas) isDirty() bool {
	if c.dirty.Load() || c.dirtyAll.Load() {
		return true
	}
	if c.refreshQueue != nil && c.refreshQueue.Len() > 0 {
		return true
	}
	return false
}

func (c *canvas) Refresh(obj fyne.CanvasObject) {
	if obj == nil {
		async.EnsureMain(c.SetDirty)
		return
	}
	c.refreshQueue.In(obj)
	if async.IsMainGoroutine() {
		async.EnsureMain(c.flushQueuedRefresh)
		return
	}
	c.markDirty()
}

func (c *canvas) markDirty() {
	c.dirty.Store(true)
	c.notifyDirty()
}

func (c *canvas) flushQueuedRefresh() {
	c.drainRefreshQueue()
	c.markDirty()
}

func (c *canvas) SetDirty() {
	c.dirtyAll.Store(true)
	c.dirty.Store(true)
	c.notifyDirty()
}

func (c *canvas) drainRefreshQueue() {
	if c.refreshQueue == nil {
		return
	}
	n := c.refreshQueue.Len()
	for i := uint64(0); i < n; i++ {
		obj := c.refreshQueue.Out()
		if obj == nil {
			return
		}
		c.unionObjectDirty(obj)
		c.dirty.Store(true)
	}
}

func (c *canvas) notifyDirty() {
	if c.onDirty != nil {
		c.onDirty()
	}
}

func (c *canvas) pixelBounds() image.Rectangle {
	return image.Rect(0, 0, scale.ToScreenCoordinate(c, c.size.Width), scale.ToScreenCoordinate(c, c.size.Height))
}

func (c *canvas) objectScreenRect(obj fyne.CanvasObject) image.Rectangle {
	pos := intdriver.AbsolutePositionForObject(obj, c.ObjectTrees())
	sz := obj.Size()
	return image.Rect(
		scale.ToScreenCoordinate(c, pos.X),
		scale.ToScreenCoordinate(c, pos.Y),
		scale.ToScreenCoordinate(c, pos.X+sz.Width),
		scale.ToScreenCoordinate(c, pos.Y+sz.Height),
	)
}

func (c *canvas) unionDirty(r image.Rectangle) {
	if c.dirtyAll.Load() || r.Empty() {
		return
	}
	if c.dirtyR.Empty() {
		c.dirtyR = r
		return
	}
	c.dirtyR = c.dirtyR.Union(r)
}

func (c *canvas) unionObjectDirty(obj fyne.CanvasObject) {
	r := c.objectScreenRect(obj)
	if c.lastBounds == nil {
		c.lastBounds = make(map[fyne.CanvasObject]image.Rectangle)
	}
	if prev, ok := c.lastBounds[obj]; ok {
		r = r.Union(prev)
	}
	c.lastBounds[obj] = c.objectScreenRect(obj)
	c.unionDirty(r)
}

func (c *canvas) takeDirty(bounds image.Rectangle) (image.Rectangle, bool) {
	if c.dirtyAll.Swap(false) {
		c.dirtyR = image.Rectangle{}
		return bounds, true
	}
	r := c.dirtyR.Intersect(bounds)
	c.dirtyR = image.Rectangle{}
	if r.Empty() || r.Eq(bounds) {
		return bounds, true
	}
	return r, false
}

func (c *canvas) renderDirty() (image.Image, image.Rectangle, bool) {
	c.drainRefreshQueue()
	bounds := c.pixelBounds()
	region, full := c.takeDirty(bounds)
	if full || c.frame == nil || !c.frame.Rect.Eq(bounds) {
		if c.frame == nil || !c.frame.Rect.Eq(bounds) {
			c.frame = image.NewNRGBA(bounds)
		}
		region = bounds
		full = true
	}
	draw.Draw(c.frame, region, image.NewUniform(theme.Color(theme.ColorNameBackground)), image.Point{}, draw.Src)
	c.rasterizer.PaintRegion(c.paintTarget(), c.frame, region)
	return c.frame, region, full
}

func (c *canvas) buildMenu(w *window, m *fyne.MainMenu) {
	c.clearMenuShortcuts()
	c.setMenuOverlay(nil)
	if m == nil {
		return
	}
	c.setMenuOverlay(buildMenuOverlay(m, w))
	c.registerMenuShortcuts(m)
}

func (c *canvas) setMenuOverlay(b fyne.CanvasObject) {
	c.menu = b
	if b != nil {
		c.menuFocusMgr = app.NewFocusManager(b)
	} else {
		c.menuFocusMgr = nil
	}
	if c.size.IsZero() {
		c.SetDirty()
		return
	}
	if c.content != nil {
		c.content.Resize(c.contentSize(c.size))
		c.content.Move(c.contentPos())
	}
	if c.menu != nil {
		c.menu.Refresh()
		c.menu.Resize(fyne.NewSize(c.size.Width, c.menu.MinSize().Height))
	}
	c.SetDirty()
}

func (c *canvas) contentPos() fyne.Position {
	pos := fyne.NewPos(0, c.menuHeight())
	if c.padded {
		return pos.Add(fyne.NewSquareOffsetPos(theme.Padding()))
	}
	return pos
}

func (c *canvas) contentSize(canvasSize fyne.Size) fyne.Size {
	sz := fyne.NewSize(canvasSize.Width, canvasSize.Height-c.menuHeight())
	if c.padded {
		return sz.Subtract(fyne.NewSquareSize(theme.Padding() * 2))
	}
	return sz
}

func (c *canvas) menuHeight() float32 {
	if c.menu == nil {
		return 0
	}
	return c.menu.MinSize().Height
}

func (c *canvas) releaseCaches() {
	for _, tree := range c.ObjectTrees() {
		if tree == nil {
			continue
		}
		intdriver.WalkCompleteObjectTree(tree, func(obj fyne.CanvasObject, _, _ fyne.Position, _ fyne.Size) bool {
			if wid, ok := obj.(fyne.Widget); ok {
				cache.DestroyRenderer(wid)
			}
			return false
		}, nil)
	}
	for {
		top := c.overlays.Top()
		if top == nil {
			break
		}
		c.overlays.Remove(top)
	}
	cache.CleanCanvas(c)
	clear(c.lastBounds)
	clear(c.minSizes)
	c.lastBounds = nil
	c.minSizes = nil
}

func (c *canvas) dropObjectCache(root fyne.CanvasObject) {
	if root == nil {
		return
	}
	intdriver.WalkCompleteObjectTree(root, func(obj fyne.CanvasObject, _, _ fyne.Position, _ fyne.Size) bool {
		delete(c.lastBounds, obj)
		delete(c.minSizes, obj)
		return false
	}, nil)
}

func (c *canvas) pruneObjectCaches() {
	if len(c.lastBounds) == 0 && len(c.minSizes) == 0 {
		return
	}
	live := make(map[fyne.CanvasObject]struct{})
	for _, tree := range c.ObjectTrees() {
		if tree == nil {
			continue
		}
		intdriver.WalkCompleteObjectTree(tree, func(obj fyne.CanvasObject, _, _ fyne.Position, _ fyne.Size) bool {
			live[obj] = struct{}{}
			return false
		}, nil)
	}
	for obj := range c.lastBounds {
		if _, ok := live[obj]; !ok {
			delete(c.lastBounds, obj)
		}
	}
	for obj := range c.minSizes {
		if _, ok := live[obj]; !ok {
			delete(c.minSizes, obj)
		}
	}
}

func (c *canvas) paintTarget() fyne.Canvas {
	if c.menu == nil {
		return c
	}
	return &menuPaintCanvas{canvas: c}
}

type menuPaintCanvas struct {
	*canvas
}

func (m *menuPaintCanvas) Content() fyne.CanvasObject {
	if m.content == nil {
		return m.menu
	}
	if m.menuRoot == nil {
		m.menuRoot = container.NewWithoutLayout(m.content, m.menu)
		return m.menuRoot
	}
	objs := m.menuRoot.Objects
	if len(objs) != 2 {
		m.menuRoot.Objects = []fyne.CanvasObject{m.content, m.menu}
		return m.menuRoot
	}
	objs[0] = m.content
	objs[1] = m.menu
	return m.menuRoot
}
