package game

import (
	"bytes"
	"fmt"
	"strings"
)

// Words is the game's string table, WORDS.TXT: string id N is line N,
// counted from 1 (VIStringTable::GetString). The disc carries one in DATA/;
// a patch server may put its own on the card, and the game reads that one
// instead — Sandstorm's adds "Register"/"Unregister" (1815, 1816) for its
// player search.
type Words []string

// ParseWords reads WORDS.TXT: UTF-8, an optional BOM, CRLF lines.
func ParseWords(data []byte) Words {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return Words(lines)
}

// Get returns string id n, or "" when the table does not have it.
func (w Words) Get(n int) string {
	if n < 1 || n > len(w) {
		return ""
	}
	return w[n-1]
}

// retailWords are the retail WORDS.TXT lines the handlers show, for when no
// table is loaded (the headless tool, tests, before the disc is read). A word
// id is its line in WORDS.TXT.
var retailWords = map[int]string{
	269:  "Character create failed",
	270:  "Bad Login/Password",
	272:  "Disconnected",
	431:  "Server not found. (DNS error)",
	519:  "Buddy invite from . . .",
	535:  "Guild invite from . . .",
	546:  "You are not qualified for that operation",
	550:  "Profanity Filter",
	551:  "On",
	552:  "Off",
	557:  "%s has accepted your invitation.",
	558:  "%s has declined your invitation.",
	559:  "%s is busy and cannot accept invitations.",
	560:  "%s cannot accept invitations.",
	563:  "Guild create failed: ",
	564:  "Guild disband failed: ",
	565:  "One or more group members are already in guilds.",
	566:  "That guild name belongs to another guild.",
	567:  "You must have at least %u Tunar to create a guild.",
	568:  "You must be the leader of a full group to create a guild.",
	570:  "You are not in a guild.",
	576:  "%s is already in a guild and cannot accept invitations.",
	578:  "The group leader has created a guild.",
	579:  "Your guild rank has changed.",
	580:  "Guild joined.",
	581:  "The guild charter has changed.",
	582:  "The guild has been disbanded.",
	583:  "You have been removed from the guild.",
	584:  "Invite failed: The guild is full.",
	586:  "%s has a full buddy list and cannot accept invitations.",
	587:  "Invitation to %s has expired.",
	589:  "Invitation failed: Too many open invites.",
	590:  "Add buddy failed: Your buddy list is full.",
	591:  "Add buddy failed: Your buddy's buddy list is full.",
	592:  "%s is already in your buddy list.",
	593:  "Not Subscribed.",
	594:  "Network Error.",
	610:  "wants to summon you, do you accept?",
	611:  "wants to resurrect you, do you accept?",
	619:  "You have logged in from another location.",
	620:  "Server not ready, please try again later. (nodbs)",
	621:  "Server not ready, please try again later. (loginto)",
	625:  "You have received more training points!",
	627:  "Recipient not found.",
	628:  "%s is already in a group.",
	630:  "The group leader has ejected you.",
	631:  "The group has been disbanded.",
	632:  "You have not been invited into this group.",
	633:  "The group is full.",
	634:  "Your group has been lost.",
	635:  "Invitation cancelled.",
	636:  "%s sends you a group invitation.  Accept?",
	642:  "%s is ignoring you.",
	647:  "Messages from your guild are visible.",
	648:  "Messages from your guild are hidden.",
	649:  "%s sends you a buddy list invitation, but you are busy.",
	650:  "%s sends you a guild invitation, but you are busy.",
	666:  "The server that you were connected to is currently unavailable.  Try back again later",
	667:  "Connection problem or server currently unavailable",
	676:  "That character is in an area of the world that is currently unavailable.  Please try again later.",
	677:  "Your character is in an area of the world that is temporarily unavailable.",
	716:  "You have been ejected by a server administrator.",
	717:  "%s has added %s to the guild.",
	718:  "We are updating the game, please try again later.",
	719:  "Disconnected (server restarted).",
	721:  "You have gained a new ability.",
	745:  "\"Member\" is the lowest rank and cannot be demoted.",
	758:  "Connecting...",
	819:  "Code",
	820:  "Logged out.",
	821:  "ISP Connection Lost.",
	822:  "Connected",
	824:  "You have memorized %S.",
	825:  "You have forgotten %S.",
	828:  "Failed: invalid name",
	829:  "Failed: name already taken",
	838:  "There is no target for assisting.",
	839:  "Your mail has been sent.",
	840:  "Message deleted.",
	1017: "Connection Problem",
	1022: "%s is still loading.",
	1024: "The server has been shutdown by a server administrator.",
	1041: "The guild has been disbanded by a server administrator.",
	1189: "Give item:",
	1196: "You are outmatched. Attacking again is not advised.",
	1420: "This character has been modified by a customer service representative.",
	1437: "You damaged your pole",
	1544: "You have scribed a new recipe.",
	1567: "You have not yet recovered.",
	1627: "You have earned %d mastery points.",
	1628: "You have earned 1 mastery point.",
	1662: "Although you are registered to play EverQuest Online Adventures: Frontiers, you are not using a Frontiers disc. You cannot access characters that are currently located in or bound to the Frontiers areas. If you would like full access to the Frontiers features, please insert a copy of EverQuest Online Adventures: Frontiers and reset your console.",
}

// word is m_ui.m_words.GetString(n): the loaded table's line, else the retail
// one, else "".
func (gs *GameState) word(n int) string {
	if s := gs.Words.Get(n); s != "" {
		return s
	}
	return retailWords[n]
}

// wordText is word(n), or "#n" for a word nobody has.
func (gs *GameState) wordText(n int) string {
	if s := gs.word(n); s != "" {
		return s
	}
	return fmt.Sprintf("#%d", n)
}
