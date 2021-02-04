package client

import "gitlab.com/NebulousLabs/Sia/modules"

// AccountingGet requests the /accounting resource
func (c *Client) AccountingGet() (ai modules.AccountingInfo, err error) {
	err = c.get("/accounting", &ai)
	return
}
