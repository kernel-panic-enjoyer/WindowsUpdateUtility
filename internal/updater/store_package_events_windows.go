//go:build windows

package updater

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	packageCatalogClassName = "Windows.ApplicationModel.PackageCatalog"
)

var (
	iidIPackageCatalog        = mustWindowsGUID("{230A3751-9DE3-4445-BE74-91FB325ABEFE}")
	iidIPackageCatalogStatics = mustWindowsGUID("{A18C9696-E65B-4634-BA21-5E63EB7244A7}")

	iidIUnknown = mustWindowsGUID("{00000000-0000-0000-C000-000000000046}")

	iidPackageCatalogInstallingHandler    = mustWindowsGUID("{A8A900C6-DA0B-5BCC-A71A-BE0B9265D87A}")
	iidPackageCatalogStagingHandler       = mustWindowsGUID("{1726F52D-2B8C-524A-98C6-F2CF0893C0F2}")
	iidPackageCatalogStatusChangedHandler = mustWindowsGUID("{B32D7D63-CD0E-5C2E-A251-FB8D290824E4}")
	iidPackageCatalogUninstallingHandler  = mustWindowsGUID("{BD636CF1-541F-53EA-8EFC-E1604A395B1A}")
	iidPackageCatalogUpdatingHandler      = mustWindowsGUID("{C23E15F6-C618-522A-82AB-4FAB36665CE5}")
)

// packageCatalogEventSource subscribes to current-user PackageCatalog changes
// only to shorten Store post-action verification latency. Events are never
// update proof; they must match the exact PFN and only trigger a fresh
// inventory/catalog check.
type packageCatalogEventSource struct{}

func (packageCatalogEventSource) Subscribe(ctx context.Context, identity StoreInstalledIdentity) (<-chan StorePackageChangeEvent, func(), error) {
	if !identity.Resolved() {
		return nil, nil, errors.New("PackageCatalog subscription requires exact Store identity")
	}
	if isAdmin() {
		// Why: Store inventory and PackageCatalog are user-scoped. An elevated
		// alternate administrator token would observe the wrong Store account and
		// could leak cross-user evidence into verification.
		return nil, nil, errors.New("PackageCatalog subscription requires the non-elevated interactive user context")
	}
	userSID, err := currentUserSID()
	if err != nil {
		return nil, nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(userSID), strings.TrimSpace(identity.UserSID)) {
		return nil, nil, fmt.Errorf("PackageCatalog user SID mismatch: process user %q, requested %q", userSID, identity.UserSID)
	}
	subscription, err := openPackageCatalogEventSubscription(ctx, identity)
	if err != nil {
		return nil, nil, err
	}
	return subscription.events, subscription.Close, nil
}

type packageCatalogSubscription struct {
	cancel context.CancelFunc
	done   chan struct{}
	events chan StorePackageChangeEvent
	once   sync.Once
}

type packageCatalogEventToken struct {
	kind  packageCatalogEventKind
	value int64
}

type packageCatalogEventKind int

const (
	packageCatalogEventStaging packageCatalogEventKind = iota
	packageCatalogEventInstalling
	packageCatalogEventUpdating
	packageCatalogEventUninstalling
	packageCatalogEventStatusChanged
)

func openPackageCatalogEventSubscription(ctx context.Context, identity StoreInstalledIdentity) (*packageCatalogSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events := make(chan StorePackageChangeEvent, 8)
	subCtx, cancel := context.WithCancel(ctx)
	subscription := &packageCatalogSubscription{
		cancel: cancel,
		done:   make(chan struct{}),
		events: events,
	}
	ready := make(chan error, 1)
	go subscription.run(subCtx, identity, ready)
	select {
	case err := <-ready:
		if err != nil {
			subscription.Close()
			return nil, err
		}
		return subscription, nil
	case <-ctx.Done():
		subscription.Close()
		return nil, ctx.Err()
	}
}

func (subscription *packageCatalogSubscription) run(ctx context.Context, identity StoreInstalledIdentity, ready chan<- error) {
	defer close(subscription.done)
	defer close(subscription.events)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := winrtInitialize(); err != nil {
		ready <- err
		return
	}
	defer winrtUninitialize()

	var catalogPtr unsafe.Pointer
	var tokens []packageCatalogEventToken
	var handlers []*packageCatalogEventHandler
	cleanup := func() {
		if catalogPtr != nil {
			catalog := (*winrtPackageCatalog)(catalogPtr)
			for _, token := range tokens {
				var remove uintptr
				switch token.kind {
				case packageCatalogEventStaging:
					remove = catalog.Vtbl.RemovePackageStaging
				case packageCatalogEventInstalling:
					remove = catalog.Vtbl.RemovePackageInstalling
				case packageCatalogEventUpdating:
					remove = catalog.Vtbl.RemovePackageUpdating
				case packageCatalogEventUninstalling:
					remove = catalog.Vtbl.RemovePackageUninstalling
				case packageCatalogEventStatusChanged:
					remove = catalog.Vtbl.RemovePackageStatusChanged
				}
				if remove != 0 {
					_ = winrtCall("IPackageCatalog event removal", remove, uintptr(catalogPtr), uintptr(token.value))
				}
			}
			winrtRelease(catalogPtr)
			catalogPtr = nil
		}
		for _, handler := range handlers {
			handler.release()
		}
		handlers = nil
	}
	defer cleanup()

	className, err := newHString(packageCatalogClassName)
	if err != nil {
		ready <- err
		return
	}
	defer className.Delete()

	var factoryPtr unsafe.Pointer
	if err := winrtCall("RoGetActivationFactory PackageCatalog", procRoGetActivationFactory.Addr(), className.Handle, uintptr(unsafe.Pointer(&iidIPackageCatalogStatics)), uintptr(unsafe.Pointer(&factoryPtr))); err != nil {
		ready <- err
		return
	}
	defer winrtRelease(factoryPtr)

	var catalogInspectable unsafe.Pointer
	factory := (*winrtPackageCatalogStatics)(factoryPtr)
	if err := winrtCall("IPackageCatalogStatics.OpenForCurrentUser", factory.Vtbl.OpenForCurrentUser, uintptr(factoryPtr), uintptr(unsafe.Pointer(&catalogInspectable))); err != nil {
		ready <- err
		return
	}
	if catalogInspectable == nil {
		ready <- errors.New("PackageCatalog.OpenForCurrentUser returned nil")
		return
	}
	catalogPtr, err = winrtQueryInterface(catalogInspectable, iidIPackageCatalog)
	winrtRelease(catalogInspectable)
	if err != nil {
		ready <- err
		return
	}
	catalog := (*winrtPackageCatalog)(catalogPtr)
	for _, kind := range []packageCatalogEventKind{packageCatalogEventUpdating, packageCatalogEventStatusChanged, packageCatalogEventInstalling, packageCatalogEventStaging, packageCatalogEventUninstalling} {
		handler := newPackageCatalogEventHandler(identity, kind, subscription.events)
		var token int64
		var add uintptr
		switch kind {
		case packageCatalogEventStaging:
			add = catalog.Vtbl.PackageStaging
		case packageCatalogEventInstalling:
			add = catalog.Vtbl.PackageInstalling
		case packageCatalogEventUpdating:
			add = catalog.Vtbl.PackageUpdating
		case packageCatalogEventUninstalling:
			add = catalog.Vtbl.PackageUninstalling
		case packageCatalogEventStatusChanged:
			add = catalog.Vtbl.PackageStatusChanged
		}
		if err := winrtCall("IPackageCatalog event subscription", add, uintptr(catalogPtr), uintptr(unsafe.Pointer(handler)), uintptr(unsafe.Pointer(&token))); err != nil {
			handler.release()
			ready <- err
			return
		}
		handlers = append(handlers, handler)
		tokens = append(tokens, packageCatalogEventToken{kind: kind, value: token})
	}
	ready <- nil
	<-ctx.Done()
}

func (subscription *packageCatalogSubscription) Close() {
	if subscription == nil {
		return
	}
	subscription.once.Do(func() {
		subscription.cancel()
		<-subscription.done
	})
}

type packageCatalogEventHandler struct {
	lpVtbl   *packageCatalogEventHandlerVtbl
	refCount int32
	identity StoreInstalledIdentity
	kind     packageCatalogEventKind
	events   chan<- StorePackageChangeEvent
}

type packageCatalogEventHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

var packageCatalogEventHandlerVtblValue = packageCatalogEventHandlerVtbl{
	QueryInterface: syscall.NewCallback(packageCatalogEventHandlerQueryInterface),
	AddRef:         syscall.NewCallback(packageCatalogEventHandlerAddRef),
	Release:        syscall.NewCallback(packageCatalogEventHandlerRelease),
	Invoke:         syscall.NewCallback(packageCatalogEventHandlerInvoke),
}

func newPackageCatalogEventHandler(identity StoreInstalledIdentity, kind packageCatalogEventKind, events chan<- StorePackageChangeEvent) *packageCatalogEventHandler {
	return &packageCatalogEventHandler{
		lpVtbl:   &packageCatalogEventHandlerVtblValue,
		refCount: 1,
		identity: identity,
		kind:     kind,
		events:   events,
	}
}

func packageCatalogEventHandlerQueryInterface(this unsafe.Pointer, iid *windows.GUID, object *uintptr) uintptr {
	if object == nil {
		return uintptr(0x80004003)
	}
	handler := (*packageCatalogEventHandler)(this)
	if iid != nil {
		if *iid != iidIUnknown && !handler.supportsIID(*iid) {
			*object = 0
			return uintptr(0x80004002)
		}
	}
	*object = uintptr(this)
	packageCatalogEventHandlerAddRef(this)
	return 0
}

func (handler *packageCatalogEventHandler) supportsIID(iid windows.GUID) bool {
	if handler == nil {
		return false
	}
	switch handler.kind {
	case packageCatalogEventStaging:
		return iid == iidPackageCatalogStagingHandler
	case packageCatalogEventInstalling:
		return iid == iidPackageCatalogInstallingHandler
	case packageCatalogEventUpdating:
		return iid == iidPackageCatalogUpdatingHandler
	case packageCatalogEventUninstalling:
		return iid == iidPackageCatalogUninstallingHandler
	case packageCatalogEventStatusChanged:
		return iid == iidPackageCatalogStatusChangedHandler
	default:
		return false
	}
}

func packageCatalogEventHandlerAddRef(this unsafe.Pointer) uintptr {
	handler := (*packageCatalogEventHandler)(this)
	return uintptr(atomic.AddInt32(&handler.refCount, 1))
}

func packageCatalogEventHandlerRelease(this unsafe.Pointer) uintptr {
	handler := (*packageCatalogEventHandler)(this)
	count := atomic.AddInt32(&handler.refCount, -1)
	return uintptr(count)
}

func (handler *packageCatalogEventHandler) release() {
	if handler != nil {
		packageCatalogEventHandlerRelease(unsafe.Pointer(handler))
	}
}

func packageCatalogEventHandlerInvoke(this unsafe.Pointer, sender unsafe.Pointer, args unsafe.Pointer) uintptr {
	handler := (*packageCatalogEventHandler)(this)
	if handler == nil || args == nil {
		return 0
	}
	event, err := packageCatalogEventFromArgs(handler.identity, handler.kind, args)
	if err != nil {
		return 0
	}
	// Why: events are a best-effort acceleration signal. Dropping a full channel
	// is safe because verification keeps polling exact inventory and catalog.
	select {
	case handler.events <- event:
	default:
	}
	return 0
}

func packageCatalogEventFromArgs(identity StoreInstalledIdentity, kind packageCatalogEventKind, args unsafe.Pointer) (StorePackageChangeEvent, error) {
	var packagePtr unsafe.Pointer
	var err error
	switch kind {
	case packageCatalogEventUpdating:
		packagePtr, err = packageFromPackageUpdatingEventArgs(args)
	case packageCatalogEventStatusChanged:
		packagePtr, err = packageFromPackageStatusChangedEventArgs(args)
	case packageCatalogEventInstalling:
		packagePtr, err = packageFromPackageInstallingEventArgs(args)
	case packageCatalogEventStaging:
		packagePtr, err = packageFromPackageStagingEventArgs(args)
	case packageCatalogEventUninstalling:
		packagePtr, err = packageFromPackageUninstallingEventArgs(args)
	}
	if err != nil {
		return StorePackageChangeEvent{}, err
	}
	if packagePtr == nil {
		return StorePackageChangeEvent{}, errors.New("PackageCatalog event had no package")
	}
	defer winrtRelease(packagePtr)
	record, err := recordFromWinRTPackage(packagePtr, identity.UserSID)
	if err != nil {
		return StorePackageChangeEvent{}, err
	}
	return StorePackageChangeEvent{
		Identity:        StoreInstalledIdentity{UserSID: identity.UserSID, PackageFamilyName: record.PackageFamilyName},
		PackageFullName: record.PackageFullName,
		Version:         record.Version.String(),
		Healthy:         record.Status.OK,
		Exists:          kind != packageCatalogEventUninstalling,
		ObservedAt:      time.Now().UTC(),
		Classification:  record.Classification,
	}, nil
}

func packageFromPackageUpdatingEventArgs(args unsafe.Pointer) (unsafe.Pointer, error) {
	eventArgs := (*winrtPackageUpdatingEventArgs)(args)
	var packagePtr unsafe.Pointer
	if err := winrtCall("IPackageUpdatingEventArgs.TargetPackage", eventArgs.Vtbl.TargetPackage, uintptr(args), uintptr(unsafe.Pointer(&packagePtr))); err != nil {
		return nil, err
	}
	return packagePtr, nil
}

func packageFromPackageStatusChangedEventArgs(args unsafe.Pointer) (unsafe.Pointer, error) {
	eventArgs := (*winrtPackageStatusChangedEventArgs)(args)
	var packagePtr unsafe.Pointer
	if err := winrtCall("IPackageStatusChangedEventArgs.Package", eventArgs.Vtbl.Package, uintptr(args), uintptr(unsafe.Pointer(&packagePtr))); err != nil {
		return nil, err
	}
	return packagePtr, nil
}

func packageFromPackageInstallingEventArgs(args unsafe.Pointer) (unsafe.Pointer, error) {
	eventArgs := (*winrtPackageProgressEventArgs)(args)
	var packagePtr unsafe.Pointer
	if err := winrtCall("IPackageInstallingEventArgs.Package", eventArgs.Vtbl.Package, uintptr(args), uintptr(unsafe.Pointer(&packagePtr))); err != nil {
		return nil, err
	}
	return packagePtr, nil
}

func packageFromPackageStagingEventArgs(args unsafe.Pointer) (unsafe.Pointer, error) {
	eventArgs := (*winrtPackageProgressEventArgs)(args)
	var packagePtr unsafe.Pointer
	if err := winrtCall("IPackageStagingEventArgs.Package", eventArgs.Vtbl.Package, uintptr(args), uintptr(unsafe.Pointer(&packagePtr))); err != nil {
		return nil, err
	}
	return packagePtr, nil
}

func packageFromPackageUninstallingEventArgs(args unsafe.Pointer) (unsafe.Pointer, error) {
	eventArgs := (*winrtPackageProgressEventArgs)(args)
	var packagePtr unsafe.Pointer
	if err := winrtCall("IPackageUninstallingEventArgs.Package", eventArgs.Vtbl.Package, uintptr(args), uintptr(unsafe.Pointer(&packagePtr))); err != nil {
		return nil, err
	}
	return packagePtr, nil
}

type winrtPackageCatalogStatics struct {
	Vtbl *winrtPackageCatalogStaticsVtbl
}

type winrtPackageCatalogStaticsVtbl struct {
	winrtInspectableVtbl
	OpenForCurrentPackage uintptr
	OpenForCurrentUser    uintptr
}

type winrtPackageCatalog struct {
	Vtbl *winrtPackageCatalogVtbl
}

type winrtPackageCatalogVtbl struct {
	winrtInspectableVtbl
	PackageStaging             uintptr
	RemovePackageStaging       uintptr
	PackageInstalling          uintptr
	RemovePackageInstalling    uintptr
	PackageUpdating            uintptr
	RemovePackageUpdating      uintptr
	PackageUninstalling        uintptr
	RemovePackageUninstalling  uintptr
	PackageStatusChanged       uintptr
	RemovePackageStatusChanged uintptr
}

type winrtPackageProgressEventArgs struct {
	Vtbl *winrtPackageProgressEventArgsVtbl
}

type winrtPackageProgressEventArgsVtbl struct {
	winrtInspectableVtbl
	ActivityID uintptr
	Package    uintptr
	Progress   uintptr
	IsComplete uintptr
	ErrorCode  uintptr
}

type winrtPackageStatusChangedEventArgs struct {
	Vtbl *winrtPackageStatusChangedEventArgsVtbl
}

type winrtPackageStatusChangedEventArgsVtbl struct {
	winrtInspectableVtbl
	Package uintptr
}

type winrtPackageUpdatingEventArgs struct {
	Vtbl *winrtPackageUpdatingEventArgsVtbl
}

type winrtPackageUpdatingEventArgsVtbl struct {
	winrtInspectableVtbl
	ActivityID    uintptr
	SourcePackage uintptr
	TargetPackage uintptr
	Progress      uintptr
	IsComplete    uintptr
	ErrorCode     uintptr
}
