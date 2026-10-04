package auth

// DisableLocalLock lets the external tests show that the store's lock alone
// serializes the password checks of an account (see noLocalLock).
func (s *Users) DisableLocalLock() { s.noLocalLock = true }

// AccountThreshold is the number of failures after which attempts must wait.
const AccountThreshold = accountThreshold
