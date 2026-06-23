//go:build windows

package updater

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	roInitMultiThreaded = 1

	packageManagerClassName = "Windows.Management.Deployment.PackageManager"
	winrtInventoryCommand   = "WinRT PackageManager.FindPackagesForUser(current user)"
)

var (
	combaseDLL                    = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize              = combaseDLL.NewProc("RoInitialize")
	procRoUninitialize            = combaseDLL.NewProc("RoUninitialize")
	procRoActivateInstance        = combaseDLL.NewProc("RoActivateInstance")
	procWindowsCreateString       = combaseDLL.NewProc("WindowsCreateString")
	procWindowsDeleteString       = combaseDLL.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuffer = combaseDLL.NewProc("WindowsGetStringRawBuffer")
	procRtlMoveMemory             = windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlMoveMemory")

	iidIPackageManager = mustWindowsGUID("{9A7D4B65-5E8F-4FC7-A2E5-7F6925CB8B53}")
	iidIPackage        = mustWindowsGUID("{163C792F-BD75-413C-BF23-B1FE7B95D825}")
	iidIPackage2       = mustWindowsGUID("{A6612FB6-7688-4ACE-95FB-359538E7AA01}")
	iidIPackage3       = mustWindowsGUID("{5F738B61-F86A-4917-93D1-F1EE9D3B35D9}")
	iidIPackage4       = mustWindowsGUID("{65AED1AE-B95B-450C-882B-6255187F397E}")
	iidIPackageStatus2 = mustWindowsGUID("{F428FA93-7C56-4862-ACFA-ABAEDCC0694D}")
	iidIStorageItem    = mustWindowsGUID("{4207A996-CA2F-42F7-BDE8-8B10457A7F30}")
)

type winrtStorePackagedAppInventoryProvider struct {
	Enumerate      func(context.Context, string) ([]StorePackagedAppRecord, error)
	CurrentUserSID func() (string, error)
	Timeout        time.Duration
}

func defaultStorePackagedAppInventoryProvider() StorePackagedAppInventoryProvider {
	return winrtStorePackagedAppInventoryProvider{}
}

func (provider winrtStorePackagedAppInventoryProvider) Inventory(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
	result := CommandResult{Command: winrtInventoryCommand}
	currentSID := currentUserSID
	if provider.CurrentUserSID != nil {
		currentSID = provider.CurrentUserSID
	}
	sid, err := currentSID()
	if err != nil {
		result.Code = 1
		result.Stderr = err.Error()
		return incompleteStorePackagedInventory(scan, err), result
	}
	if !strings.EqualFold(strings.TrimSpace(sid), strings.TrimSpace(scan.UserSID)) {
		err := fmt.Errorf("Store inventory user SID mismatch: process user %q, scan user %q", sid, scan.UserSID)
		result.Code = 1
		result.Stderr = err.Error()
		return incompleteStorePackagedInventory(scan, err), result
	}

	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = storePackagedInventoryTimeout
	}
	enum := provider.Enumerate
	if enum == nil {
		enum = enumerateWinRTCurrentUserPackages
	}
	enumCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type inventoryResult struct {
		records []StorePackagedAppRecord
		err     error
	}
	done := make(chan inventoryResult, 1)
	go func() {
		records, err := enum(enumCtx, scan.UserSID)
		done <- inventoryResult{records: records, err: err}
	}()

	var records []StorePackagedAppRecord
	select {
	case <-enumCtx.Done():
		err = enumCtx.Err()
	case output := <-done:
		records = output.records
		err = output.err
	}
	if err != nil {
		result.Code = 1
		result.Stderr = err.Error()
		return incompleteStorePackagedInventory(scan, err), result
	}

	normalized := make([]StorePackagedAppRecord, 0, len(records))
	for _, record := range records {
		item, normalizeErr := normalizeStorePackagedAppRecord(record, scan.UserSID)
		if normalizeErr != nil {
			result.Code = 2
			result.Stderr = normalizeErr.Error()
			return incompleteStorePackagedInventory(scan, normalizeErr), result
		}
		normalized = append(normalized, item)
	}

	scan.CompletedAt = time.Now().UTC()
	scan.CompletionStatus = StoreScanCompleted
	inventory := StorePackagedAppInventory{
		Scan:     scan,
		Records:  normalized,
		Families: groupStorePackagedAppFamilies(normalized),
	}
	result.OK = true
	result.Stdout = fmt.Sprintf("WinRT Store inventory returned %d package record(s), %d product-like family group(s).", len(inventory.Records), productLikeFamilyCount(inventory.Families))
	return inventory, result
}

func incompleteStorePackagedInventory(scan StoreScanGeneration, err error) StorePackagedAppInventory {
	scan.CompletedAt = time.Now().UTC()
	scan.CompletionStatus = StoreScanIncomplete
	return StorePackagedAppInventory{
		Scan:    scan,
		Partial: true,
		Errors:  []string{err.Error()},
	}
}

func enumerateWinRTCurrentUserPackages(ctx context.Context, userSID string) ([]StorePackagedAppRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := winrtInitialize(); err != nil {
		return nil, err
	}
	defer winrtUninitialize()

	className, err := newHString(packageManagerClassName)
	if err != nil {
		return nil, err
	}
	defer className.Delete()

	var inspectable unsafe.Pointer
	if err := winrtCall("RoActivateInstance PackageManager", procRoActivateInstance.Addr(), className.Handle, uintptr(unsafe.Pointer(&inspectable))); err != nil {
		return nil, err
	}
	defer winrtRelease(inspectable)

	packageManager, err := winrtQueryInterface(inspectable, iidIPackageManager)
	if err != nil {
		return nil, err
	}
	defer winrtRelease(packageManager)

	user, err := newHString("")
	if err != nil {
		return nil, err
	}
	defer user.Delete()

	var iterablePtr unsafe.Pointer
	manager := (*winrtPackageManager)(packageManager)
	if err := winrtCall("IPackageManager.FindPackagesForUser", manager.Vtbl.FindPackagesForUser, uintptr(packageManager), user.Handle, uintptr(unsafe.Pointer(&iterablePtr))); err != nil {
		return nil, err
	}
	if iterablePtr == nil {
		return nil, errors.New("PackageManager.FindPackagesForUser returned no package iterable")
	}
	defer winrtRelease(iterablePtr)

	var iteratorPtr unsafe.Pointer
	iterable := (*winrtIterable)(iterablePtr)
	if err := winrtCall("IIterable<Package>.First", iterable.Vtbl.First, uintptr(iterablePtr), uintptr(unsafe.Pointer(&iteratorPtr))); err != nil {
		return nil, err
	}
	if iteratorPtr == nil {
		return nil, errors.New("PackageManager.FindPackagesForUser returned no package iterator")
	}
	defer winrtRelease(iteratorPtr)

	iterator := (*winrtPackageIterator)(iteratorPtr)
	records := []StorePackagedAppRecord{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hasCurrent, err := winrtGetBool(iterator.Vtbl.GetHasCurrent, iteratorPtr, "IIterator<Package>.HasCurrent")
		if err != nil {
			return nil, err
		}
		if !hasCurrent {
			break
		}

		var packagePtr unsafe.Pointer
		if err := winrtCall("IIterator<Package>.Current", iterator.Vtbl.GetCurrent, uintptr(iteratorPtr), uintptr(unsafe.Pointer(&packagePtr))); err != nil {
			return nil, err
		}
		if packagePtr != nil {
			record, recordErr := recordFromWinRTPackage(packagePtr, userSID)
			winrtRelease(packagePtr)
			if recordErr != nil {
				return nil, recordErr
			}
			records = append(records, record)
		}

		moved, err := winrtGetBool(iterator.Vtbl.MoveNext, iteratorPtr, "IIterator<Package>.MoveNext")
		if err != nil {
			return nil, err
		}
		if !moved {
			break
		}
	}
	return records, nil
}

func recordFromWinRTPackage(packageObject unsafe.Pointer, userSID string) (StorePackagedAppRecord, error) {
	packagePtr, err := winrtQueryInterface(packageObject, iidIPackage)
	if err != nil {
		return StorePackagedAppRecord{}, err
	}
	defer winrtRelease(packagePtr)

	packageValue := (*winrtPackage)(packagePtr)
	var packageIDPtr unsafe.Pointer
	if err := winrtCall("IPackage.Id", packageValue.Vtbl.GetID, uintptr(packagePtr), uintptr(unsafe.Pointer(&packageIDPtr))); err != nil {
		return StorePackagedAppRecord{}, err
	}
	if packageIDPtr == nil {
		return StorePackagedAppRecord{}, errors.New("Package.Id returned nil")
	}
	defer winrtRelease(packageIDPtr)

	packageID := (*winrtPackageID)(packageIDPtr)
	version, err := winrtGetPackageVersion(packageID.Vtbl.GetVersion, packageIDPtr)
	if err != nil {
		return StorePackagedAppRecord{}, err
	}
	architecture, err := winrtGetInt32(packageID.Vtbl.GetArchitecture, packageIDPtr, "IPackageId.Architecture")
	if err != nil {
		return StorePackagedAppRecord{}, err
	}
	record := StorePackagedAppRecord{
		UserSID:               userSID,
		PackageFamilyName:     mustWinRTString(packageID.Vtbl.GetFamilyName, packageIDPtr),
		PackageFullName:       mustWinRTString(packageID.Vtbl.GetFullName, packageIDPtr),
		IdentityName:          mustWinRTString(packageID.Vtbl.GetName, packageIDPtr),
		Publisher:             mustWinRTString(packageID.Vtbl.GetPublisher, packageIDPtr),
		PublisherID:           mustWinRTString(packageID.Vtbl.GetPublisherID, packageIDPtr),
		Version:               version,
		ProcessorArchitecture: packageProcessorArchitectureName(architecture),
		PackageType:           "Windows.ApplicationModel.Package",
		IsFramework:           mustWinRTBool(packageValue.Vtbl.GetIsFramework, packagePtr),
		Status:                StorePackageStatus{},
	}

	if location := packageInstallLocation(packageValue, packagePtr); location != "" {
		record.InstallLocation = location
	}
	if package2Ptr, err := winrtQueryInterface(packageObject, iidIPackage2); err == nil {
		package2 := (*winrtPackage2)(package2Ptr)
		record.DisplayName = mustWinRTString(package2.Vtbl.GetDisplayName, package2Ptr)
		record.IsResourcePackage = mustWinRTBool(package2.Vtbl.GetIsResourcePackage, package2Ptr)
		record.IsBundle = mustWinRTBool(package2.Vtbl.GetIsBundle, package2Ptr)
		record.IsDevelopmentMode = mustWinRTBool(package2.Vtbl.GetIsDevelopmentMode, package2Ptr)
		winrtRelease(package2Ptr)
	}
	if package4Ptr, err := winrtQueryInterface(packageObject, iidIPackage4); err == nil {
		package4 := (*winrtPackage4)(package4Ptr)
		record.IsOptional = mustWinRTBool(package4.Vtbl.GetIsOptional, package4Ptr)
		winrtRelease(package4Ptr)
	}
	if package3Ptr, err := winrtQueryInterface(packageObject, iidIPackage3); err == nil {
		package3 := (*winrtPackage3)(package3Ptr)
		var statusPtr unsafe.Pointer
		if err := winrtCall("IPackage3.Status", package3.Vtbl.GetStatus, uintptr(package3Ptr), uintptr(unsafe.Pointer(&statusPtr))); err == nil && statusPtr != nil {
			record.Status = packageStatusFromWinRT(statusPtr)
			record.IsStaged = record.Status.IsPartiallyStaged
			winrtRelease(statusPtr)
		}
		winrtRelease(package3Ptr)
	}
	record.Classification = classifyStorePackagedApp(record)
	return record, nil
}

func packageInstallLocation(packageValue *winrtPackage, packagePtr unsafe.Pointer) string {
	var locationPtr unsafe.Pointer
	if err := winrtCall("IPackage.InstalledLocation", packageValue.Vtbl.GetInstalledLocation, uintptr(packagePtr), uintptr(unsafe.Pointer(&locationPtr))); err != nil || locationPtr == nil {
		return ""
	}
	defer winrtRelease(locationPtr)
	itemPtr, err := winrtQueryInterface(locationPtr, iidIStorageItem)
	if err != nil {
		return ""
	}
	defer winrtRelease(itemPtr)
	item := (*winrtStorageItem)(itemPtr)
	return mustWinRTString(item.Vtbl.GetPath, itemPtr)
}

func packageStatusFromWinRT(statusPtr unsafe.Pointer) StorePackageStatus {
	statusValue := (*winrtPackageStatus)(statusPtr)
	status := StorePackageStatus{}
	if ok, err := winrtGetBool(statusValue.Vtbl.VerifyIsOK, statusPtr, "IPackageStatus.VerifyIsOK"); err == nil {
		status.OK = ok
	} else {
		status.VerifyError = err.Error()
	}
	status.NotAvailable = mustWinRTBool(statusValue.Vtbl.GetNotAvailable, statusPtr)
	status.PackageOffline = mustWinRTBool(statusValue.Vtbl.GetPackageOffline, statusPtr)
	status.DataOffline = mustWinRTBool(statusValue.Vtbl.GetDataOffline, statusPtr)
	status.Disabled = mustWinRTBool(statusValue.Vtbl.GetDisabled, statusPtr)
	status.NeedsRemediation = mustWinRTBool(statusValue.Vtbl.GetNeedsRemediation, statusPtr)
	status.LicenseIssue = mustWinRTBool(statusValue.Vtbl.GetLicenseIssue, statusPtr)
	status.Modified = mustWinRTBool(statusValue.Vtbl.GetModified, statusPtr)
	status.Tampered = mustWinRTBool(statusValue.Vtbl.GetTampered, statusPtr)
	status.DependencyIssue = mustWinRTBool(statusValue.Vtbl.GetDependencyIssue, statusPtr)
	status.Servicing = mustWinRTBool(statusValue.Vtbl.GetServicing, statusPtr)
	status.DeploymentInProgress = mustWinRTBool(statusValue.Vtbl.GetDeploymentInProgress, statusPtr)
	if status2Ptr, err := winrtQueryInterface(statusPtr, iidIPackageStatus2); err == nil {
		status2 := (*winrtPackageStatus2)(status2Ptr)
		status.IsPartiallyStaged = mustWinRTBool(status2.Vtbl.GetIsPartiallyStaged, status2Ptr)
		winrtRelease(status2Ptr)
	}
	return status
}

func winrtInitialize() error {
	if err := winrtCall("RoInitialize", procRoInitialize.Addr(), roInitMultiThreaded); err != nil {
		return err
	}
	return nil
}

func winrtUninitialize() {
	_, _, _ = syscall.SyscallN(procRoUninitialize.Addr())
}

func winrtQueryInterface(object unsafe.Pointer, iid windows.GUID) (unsafe.Pointer, error) {
	if object == nil {
		return nil, errors.New("QueryInterface called with nil object")
	}
	var out unsafe.Pointer
	unknown := (*winrtUnknown)(object)
	if err := winrtCall("QueryInterface", unknown.Vtbl.QueryInterface, uintptr(object), uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out))); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("QueryInterface returned nil")
	}
	return out, nil
}

func winrtRelease(object unsafe.Pointer) {
	if object == nil {
		return
	}
	unknown := (*winrtUnknown)(object)
	_, _, _ = syscall.SyscallN(unknown.Vtbl.Release, uintptr(object))
}

func winrtCall(label string, proc uintptr, args ...uintptr) error {
	hr, _, _ := syscall.SyscallN(proc, args...)
	if failedHRESULT(hr) {
		return fmt.Errorf("%s failed: HRESULT 0x%08X", label, uint32(hr))
	}
	return nil
}

func failedHRESULT(hr uintptr) bool {
	return int32(uint32(hr)) < 0
}

func mustWindowsGUID(value string) windows.GUID {
	guid, err := windows.GUIDFromString(value)
	if err != nil {
		panic(err)
	}
	return guid
}

type hstring struct {
	Handle uintptr
}

func newHString(value string) (hstring, error) {
	utf16Value, err := syscall.UTF16FromString(value)
	if err != nil {
		return hstring{}, err
	}
	var handle uintptr
	var ptr uintptr
	if len(utf16Value) > 0 {
		ptr = uintptr(unsafe.Pointer(&utf16Value[0]))
	}
	if err := winrtCall("WindowsCreateString", procWindowsCreateString.Addr(), ptr, uintptr(len(utf16Value)-1), uintptr(unsafe.Pointer(&handle))); err != nil {
		return hstring{}, err
	}
	return hstring{Handle: handle}, nil
}

func (value hstring) Delete() {
	if value.Handle != 0 {
		_, _, _ = syscall.SyscallN(procWindowsDeleteString.Addr(), value.Handle)
	}
}

func winrtHStringToString(handle uintptr) string {
	if handle == 0 {
		return ""
	}
	var length uint32
	raw, _, _ := syscall.SyscallN(procWindowsGetStringRawBuffer.Addr(), handle, uintptr(unsafe.Pointer(&length)))
	if raw == 0 || length == 0 {
		return ""
	}
	buffer := make([]uint16, length)
	_, _, _ = syscall.SyscallN(procRtlMoveMemory.Addr(), uintptr(unsafe.Pointer(&buffer[0])), raw, uintptr(length)*2)
	return string(utf16.Decode(buffer))
}

func winrtGetString(method uintptr, receiver unsafe.Pointer, label string) (string, error) {
	var value uintptr
	if err := winrtCall(label, method, uintptr(receiver), uintptr(unsafe.Pointer(&value))); err != nil {
		return "", err
	}
	defer func() {
		if value != 0 {
			_, _, _ = syscall.SyscallN(procWindowsDeleteString.Addr(), value)
		}
	}()
	return winrtHStringToString(value), nil
}

func mustWinRTString(method uintptr, receiver unsafe.Pointer) string {
	value, _ := winrtGetString(method, receiver, "WinRT string getter")
	return value
}

func winrtGetBool(method uintptr, receiver unsafe.Pointer, label string) (bool, error) {
	var value byte
	if err := winrtCall(label, method, uintptr(receiver), uintptr(unsafe.Pointer(&value))); err != nil {
		return false, err
	}
	return value != 0, nil
}

func mustWinRTBool(method uintptr, receiver unsafe.Pointer) bool {
	value, _ := winrtGetBool(method, receiver, "WinRT boolean getter")
	return value
}

func winrtGetInt32(method uintptr, receiver unsafe.Pointer, label string) (int32, error) {
	var value int32
	if err := winrtCall(label, method, uintptr(receiver), uintptr(unsafe.Pointer(&value))); err != nil {
		return 0, err
	}
	return value, nil
}

func winrtGetPackageVersion(method uintptr, receiver unsafe.Pointer) (StorePackageVersion, error) {
	var value StorePackageVersion
	if err := winrtCall("IPackageId.Version", method, uintptr(receiver), uintptr(unsafe.Pointer(&value))); err != nil {
		return StorePackageVersion{}, err
	}
	return value, nil
}

func packageProcessorArchitectureName(value int32) string {
	switch value {
	case 0:
		return "X86"
	case 5:
		return "Arm"
	case 9:
		return "X64"
	case 11:
		return "Neutral"
	case 12:
		return "Arm64"
	case 14:
		return "X86OnArm64"
	case 0xffff:
		return "Unknown"
	default:
		return fmt.Sprintf("Architecture(%d)", value)
	}
}

type winrtUnknown struct {
	Vtbl *winrtUnknownVtbl
}

type winrtUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type winrtInspectableVtbl struct {
	QueryInterface      uintptr
	AddRef              uintptr
	Release             uintptr
	GetIids             uintptr
	GetRuntimeClassName uintptr
	GetTrustLevel       uintptr
}

type winrtPackageManager struct {
	Vtbl *winrtPackageManagerVtbl
}

type winrtPackageManagerVtbl struct {
	winrtInspectableVtbl
	AddPackageAsync       uintptr
	UpdatePackageAsync    uintptr
	RemovePackageAsync    uintptr
	StagePackageAsync     uintptr
	RegisterPackageAsync  uintptr
	FindPackages          uintptr
	FindPackagesForUser   uintptr
	FindPackages2         uintptr
	FindPackagesForUser2  uintptr
	FindUsers             uintptr
	SetPackageState       uintptr
	FindPackage           uintptr
	CleanupPackageForUser uintptr
	FindPackages3         uintptr
	FindPackagesForUser3  uintptr
	FindPackageForUser    uintptr
}

type winrtIterable struct {
	Vtbl *winrtIterableVtbl
}

type winrtIterableVtbl struct {
	winrtInspectableVtbl
	First uintptr
}

type winrtPackageIterator struct {
	Vtbl *winrtPackageIteratorVtbl
}

type winrtPackageIteratorVtbl struct {
	winrtInspectableVtbl
	GetCurrent    uintptr
	GetHasCurrent uintptr
	MoveNext      uintptr
	GetMany       uintptr
}

type winrtPackage struct {
	Vtbl *winrtPackageVtbl
}

type winrtPackageVtbl struct {
	winrtInspectableVtbl
	GetID                uintptr
	GetInstalledLocation uintptr
	GetIsFramework       uintptr
	GetDependencies      uintptr
}

type winrtPackage2 struct {
	Vtbl *winrtPackage2Vtbl
}

type winrtPackage2Vtbl struct {
	winrtInspectableVtbl
	GetDisplayName          uintptr
	GetPublisherDisplayName uintptr
	GetDescription          uintptr
	GetLogo                 uintptr
	GetIsResourcePackage    uintptr
	GetIsBundle             uintptr
	GetIsDevelopmentMode    uintptr
}

type winrtPackage3 struct {
	Vtbl *winrtPackage3Vtbl
}

type winrtPackage3Vtbl struct {
	winrtInspectableVtbl
	GetStatus              uintptr
	GetInstalledDate       uintptr
	GetAppListEntriesAsync uintptr
}

type winrtPackage4 struct {
	Vtbl *winrtPackage4Vtbl
}

type winrtPackage4Vtbl struct {
	winrtInspectableVtbl
	GetSignatureKind            uintptr
	GetIsOptional               uintptr
	VerifyContentIntegrityAsync uintptr
}

type winrtPackageID struct {
	Vtbl *winrtPackageIDVtbl
}

type winrtPackageIDVtbl struct {
	winrtInspectableVtbl
	GetName         uintptr
	GetVersion      uintptr
	GetArchitecture uintptr
	GetResourceID   uintptr
	GetPublisher    uintptr
	GetPublisherID  uintptr
	GetFullName     uintptr
	GetFamilyName   uintptr
}

type winrtPackageStatus struct {
	Vtbl *winrtPackageStatusVtbl
}

type winrtPackageStatusVtbl struct {
	winrtInspectableVtbl
	VerifyIsOK              uintptr
	GetNotAvailable         uintptr
	GetPackageOffline       uintptr
	GetDataOffline          uintptr
	GetDisabled             uintptr
	GetNeedsRemediation     uintptr
	GetLicenseIssue         uintptr
	GetModified             uintptr
	GetTampered             uintptr
	GetDependencyIssue      uintptr
	GetServicing            uintptr
	GetDeploymentInProgress uintptr
}

type winrtPackageStatus2 struct {
	Vtbl *winrtPackageStatus2Vtbl
}

type winrtPackageStatus2Vtbl struct {
	winrtInspectableVtbl
	GetIsPartiallyStaged uintptr
}

type winrtStorageItem struct {
	Vtbl *winrtStorageItemVtbl
}

type winrtStorageItemVtbl struct {
	winrtInspectableVtbl
	RenameAsync        uintptr
	RenameAsync2       uintptr
	DeleteAsync        uintptr
	DeleteAsync2       uintptr
	GetBasicProperties uintptr
	GetName            uintptr
	GetPath            uintptr
	GetAttributes      uintptr
	GetDateCreated     uintptr
	IsOfType           uintptr
}
