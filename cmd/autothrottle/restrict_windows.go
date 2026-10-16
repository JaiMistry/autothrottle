package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// restrictDataDir keeps the data folder from other users of the machine:
// it holds the password hash, the encrypted credentials and the key
// beside them. ProgramData lets every user read what is in it, so the
// folder stops inheriting that and is given to SYSTEM, Administrators,
// the service's own account and whoever is running this. Windows applies
// the new list to what is already inside, and to what is added later.
func restrictDataDir(dir string) error {
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;LS)"
	if user, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		sddl += "(A;OICI;FA;;;" + user.User.Sid.String() + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
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
