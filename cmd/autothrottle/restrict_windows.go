package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// serviceSIDName is the service's own identity. The service runs as
// LocalService, which many services share; this SID only it holds.
const serviceSIDName = `NT SERVICE\AutoThrottle`

// restrictDataDir keeps the data folder from other users of the machine:
// it holds the password hash, the encrypted credentials and the key
// beside them. ProgramData lets every user read what is in it, so the
// folder stops inheriting that and is given to SYSTEM, Administrators,
// the service's own identity and whoever is running this. Windows applies
// the new list to what is already inside, and to what is added later.
func restrictDataDir(dir string) error {
	dacl, err := dataDirDACL()
	if err != nil {
		return err
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("restricting %s: %w", dir, err)
	}
	return nil
}

// claimDataDir is restrictDataDir for the installer, which runs as an
// administrator: it also makes Administrators the folder's owner. Any user
// can create folders in ProgramData, and an owner can always change the
// permissions again, so a folder someone else made first is refused, as
// is one that is a link to somewhere else.
func claimDataDir(dir string) error {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(path)
	switch {
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		if err := os.Mkdir(dir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	case err != nil:
		return fmt.Errorf("reading %s: %w", dir, err)
	case attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
		return fmt.Errorf("%s is a link to another folder. Delete it, then install again", dir)
	case attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0:
		return fmt.Errorf("%s is a file, not a folder. Delete it, then install again", dir)
	default:
		if err := checkOwner(dir); err != nil {
			return err
		}
	}

	dacl, err := dataDirDACL()
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		admins, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("securing %s: %w", dir, err)
	}
	return nil
}

// checkOwner accepts a folder owned by SYSTEM, Administrators, the
// service, or the administrator installing now: anyone else made it.
func checkOwner(dir string) error {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("reading the owner of %s: %w", dir, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	for _, sid := range trustedSIDs() {
		if owner.Equals(sid) {
			return nil
		}
	}
	name := owner.String()
	if account, domain, _, err := owner.LookupAccount(""); err == nil {
		name = domain + `\` + account
	}
	return fmt.Errorf("%s already exists and belongs to %s, not an administrator. Delete it, or make Administrators its owner, then install again", dir, name)
}

// trustedSIDs are the accounts the data folder belongs to.
func trustedSIDs() []*windows.SID {
	var sids []*windows.SID
	for _, known := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		if sid, err := windows.CreateWellKnownSid(known); err == nil {
			sids = append(sids, sid)
		}
	}
	if sid, _, _, err := windows.LookupSID("", serviceSIDName); err == nil {
		sids = append(sids, sid)
	}
	// Whoever runs this, unless that is the shared LocalService account:
	// the service is let in by its own identity instead.
	if user, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil && !user.User.Sid.IsWellKnown(windows.WinLocalServiceSid) {
		sids = append(sids, user.User.Sid)
	}
	return sids
}

func dataDirDACL() (*windows.ACL, error) {
	sddl := "D:P"
	for _, sid := range trustedSIDs() {
		sddl += "(A;OICI;FA;;;" + sid.String() + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	dacl, _, err := sd.DACL()
	return dacl, err
}
