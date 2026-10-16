package main

import (
	"log"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// serverDisconnected is the visual client's half of a server-ordered
// VIClient::Disconnect (game.SessionIntake.Disconnect, intake_session.go).
// CLIENT closes the connection and destroys DRDP without sending an opcode of
// its own, so this does not send 0x09B0 the way disconnectAndReturnToLogin
// does. The PS2 then shows ConnectStatusString's text and, once dismissed,
// goes back to its login window (DisconnectKind 2) or leaves the client
// (3, 4); here every kind lands on the login screen with the text as its
// error line. A quiet disconnect (0x0777) shows nothing.
func (c *Client) serverDisconnected() {
	gs := c.engine.State
	d := gs.Session.Disconnect
	log.Printf("Server disconnect: reason %d quiet=%v -> login screen (%q)", d.Reason, d.Quiet, d.Text)
	if gs.MemoryDumpCount > 0 {
		c.saveChar()
	}
	if gs.Conn != nil {
		gs.Conn.Close()
	}
	c.engine = nil
	c.loginError = d.Text
	c.logoutActive = false
	c.menuOpen = false
	c.whoRequested = false
	c.auctionRequested = false
	c.auctionPage = 0
	c.charSelectIdx = -1
	screenWidth = 640
	screenHeight = 480
	rl.SetWindowSize(640, 480)
}
