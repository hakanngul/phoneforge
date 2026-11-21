package farms

import (
	"context"
	"errors"
)

// ErrAWSNotImplemented is returned by AWS Device Farm listing.
var ErrAWSNotImplemented = errors.New("AWS Device Farm listing requires AWS SDK (not bundled); configure credentials for later use")

// listAWSDeviceFarm is a STUB. The AWS Device Farm API requires SigV4 request
// signing, which is not implemented here to avoid pulling in the AWS SDK.
// Credentials (access_key_id, secret_access_key, optional region) can still be
// stored through Configure for later use, but ListDevices always returns
// ErrAWSNotImplemented. UNIMPLEMENTED: SigV4 signing and the ListDevices call.
func (m *Manager) listAWSDeviceFarm(_ context.Context) ([]CloudDevice, error) {
	return nil, ErrAWSNotImplemented
}
