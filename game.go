package riverboat

import (
	"math"
	"sort"
	"sync"

	. "github.com/alexclewontin/riverboat/eval"
)

// MaxSeats: I mean, if you really want to...
const MaxPlayers = 23

// Heads up!
const MinPlayers = 2

type GameFlags uint8

/*
xxxxBSSS
--------
xxxxxxxx

SSS - Status
	000 : Nothing
	001 : PreDeal
	010 : PreFlop
	011 : Flop
	100 : Turn
	101 : River
	110 : Over

B - Betting
	1 :Yes, still betting
	0: No, can advance
*/

type GameStage uint8

const (
	PreDeal GameStage = iota + 1
	PreFlop
	Flop
	Turn
	River
	Over
)

type Pot struct {
	TopShare          uint
	Amt               uint
	EligblePlayerNums []uint
	WinningPlayerNums []uint
	WinningHand       []Card
	WinningScore      int
}

type GameConfig struct {
	MaxBuy     uint
	BigBlind   uint
	SmallBlind uint
}

type Game struct {
	mtx sync.Mutex

	dealerNum      uint
	actionNum      uint
	utgNum         uint
	sbNum          uint
	bbNum          uint
	communityCards []Card
	flags          GameFlags
	config         GameConfig
	players        []player
	deck           Deck
	pots           []Pot
	minRaise       uint
}

func (g *Game) getStage() GameStage {
	return GameStage(g.flags & 0x07)
}

func (g *Game) getBetting() bool {
	return (g.flags&0x08 == 0x08)
}

func (g *Game) getStageAndBetting() (GameStage, bool) {
	return g.getStage(), g.getBetting()
}

func (g *Game) setStage(s GameStage) {
	g.flags = GameFlags((uint8(g.flags) & 0xF8) | uint8(s))
}

func (g *Game) setBetting(b bool) {
	if b {
		g.flags = GameFlags(uint8(g.flags) | 0x08)
	} else {
		g.flags = GameFlags(uint8(g.flags) & 0xF7)
	}
}

func (g *Game) setStageAndBetting(s GameStage, b bool) {
	g.setStage(s)
	g.setBetting(b)
}

func (g *Game) getPlayer(pn uint) (*player, error) {
	if pn >= uint(len(g.players)) {
		return nil, ErrBadPlayerNum
	}
	return &g.players[pn], nil
}

func (g *Game) readyCount() uint {
	var readyCount uint = 0
	for _, p := range g.players {
		if p.Ready {
			readyCount++
		}
	}
	return readyCount
}

func (g *Game) isCalled(pn uint) bool {
	return g.players[pn].allIn() || (g.players[pn].Bet == g.toCall())
}

//This function is called for every stage except PreDeal, when the setup works differently
func (g *Game) initStage() {
	g.actionNum = (g.dealerNum + 1) % uint(len(g.players))
	for !g.players[g.actionNum].In {
		g.actionNum = (g.actionNum + 1) % uint(len(g.players))
	}

	for i := range g.players {
		g.players[i].Bet = 0
	}

	g.minRaise = g.config.BigBlind

	//TODO: if all or all but one are all-in and its not the end, don't set betting to true on the next deal
}

//Returns nil if there are more than 2 players ready, ErrNotEnoughPlayers otherwise
func (g *Game) updateBlindNums() error {
	readyCount := g.readyCount()

	if readyCount < 2 {
		g.bbNum = g.dealerNum
		g.sbNum = g.dealerNum
		g.utgNum = g.dealerNum
		return ErrNotEnoughPlayers

	} else if readyCount == 2 {
		g.sbNum = g.dealerNum
		g.utgNum = g.dealerNum
		g.bbNum = (g.dealerNum + 1) % uint(len(g.players))
		for !g.players[g.bbNum].Ready {
			g.bbNum = (g.bbNum + 1) % uint(len(g.players))
		}
	} else {
		g.sbNum = (g.dealerNum + 1) % uint(len(g.players))
		for !g.players[g.sbNum].Ready {
			g.sbNum = (g.sbNum + 1) % uint(len(g.players))
		}

		g.bbNum = (g.sbNum + 1) % uint(len(g.players))
		for !g.players[g.bbNum].Ready {
			g.bbNum = (g.bbNum + 1) % uint(len(g.players))
		}

		g.utgNum = (g.bbNum + 1) % uint(len(g.players))
		for !g.players[g.utgNum].Ready {
			g.utgNum = (g.utgNum + 1) % uint(len(g.players))
		}
	}

	return nil
}

func (g *Game) toCall() uint {
	var val uint = 0

	for _, q := range g.players {
		if q.Bet > val {
			val = q.Bet
		}
	}

	return val
}

func (g *Game) getLimit() uint {
	//TODO: implement limits
	return uint(math.MaxUint64)
}

func (g *Game) canOpen(pn uint) bool {
	//TODO: placeholder stub, as limits on who can open betting will eventually be implemented
	return true
}

func (g *Game) resetForNextHand() {

	for i := range g.players {
		g.players[i].In = false
		g.players[i].Bet = 0
		g.players[i].TotalBet = 0

		if g.players[i].Stack == 0 {
			g.players[i].Ready = false
		}

	}

	g.dealerNum = g.dealerNum + 1
	for !g.players[g.dealerNum].Ready {
		g.dealerNum = (g.dealerNum + 1) % uint(len(g.players))
	}

	g.setStageAndBetting(PreDeal, false)
}

func (g *Game) updateRoundInfo() error {

	var allCalled = true
	var allInPlayerNums = []uint{}
	var inPlayerNums = []uint{}

	for i, p := range g.players {
		if p.In {
			inPlayerNums = append(inPlayerNums, uint(i))
			if p.allIn() {
				allInPlayerNums = append(allInPlayerNums, uint(i))
			} else if !g.isCalled(uint(i)) {
				allCalled = false
			}
		}
	}

	// If less than two players are still in, the hand has been conceded
	if len(inPlayerNums) < 2 {
		//the sole number in the array is the winner by default
		g.setStageAndBetting(PreDeal, false)

		//TODO: Create a pot here to simplify sending result description
		for _, p := range g.players {
			g.players[inPlayerNums[0]].Stack += p.TotalBet
		}

		return nil
	}

	// If two or more players are in, but not everybody has called
	if !allCalled {
		// just move action to next player
		for g.isCalled(g.actionNum) || g.players[g.actionNum].allIn() || !g.players[g.actionNum].In {
			g.actionNum = (g.actionNum + 1) % uint(len(g.players))
		}

		return nil
	}

	//If there are two or more players in, and everybody has either called or is all-in, and at this point we determine that only one player is
	//in but not all in, we take all the money above and beyond the second highest better (who is all in) and return it to the people who bet it
	//If the only players in are both all in for the exact same amount of money, nothing happens here
	//(but we can't skip in the "0 not all in" case because technically before this step happens) a player who after this step may read as not all in
	//could return true for the isAllIn method
	if len(inPlayerNums)-len(allInPlayerNums) < 2 {
		var topBettor1 uint = 0
		var topBettor2 uint = 0

		for _, ndx := range inPlayerNums {
			if g.players[ndx].TotalBet > g.players[topBettor1].TotalBet {
				topBettor2 = topBettor1
				topBettor1 = ndx
			} else if g.players[ndx].TotalBet > g.players[topBettor2].TotalBet {
				topBettor2 = ndx
			}
		}

		g.players[topBettor1].returnChips(g.players[topBettor1].TotalBet - g.players[topBettor2].TotalBet)
	}

	//If there are two or more players in, and everybody has called or is all in, then end the hand f we've just finished river betting
	if g.getStage() == River {

		sort.Slice(allInPlayerNums, func(i, j int) bool {
			return g.players[allInPlayerNums[i]].TotalBet < g.players[allInPlayerNums[j]].TotalBet
		}) //here, the whole slice needs to be sorted by the totalBet amount of the players represented

		var pots []Pot
		for _, ndx := range allInPlayerNums {

			newPot := Pot{}
			newPot.TopShare = g.players[ndx].TotalBet

			for i := range g.players {

				if g.players[i].TotalBet >= newPot.TopShare {
					if g.players[i].In {
						newPot.EligblePlayerNums = append(newPot.EligblePlayerNums, uint(i))
					}
					newPot.Amt += newPot.TopShare
					g.players[i].TotalBet -= newPot.TopShare
				} else {
					newPot.Amt += g.players[i].TotalBet
					g.players[i].TotalBet = 0
				}
			}

			pots = append(pots, newPot)
		}

		for i := range pots {
			pots[i].WinningScore = 8000

			for _, num := range pots[i].EligblePlayerNums {

				hand, score := BestFiveOfSeven(
					g.players[num].Cards[0],
					g.players[num].Cards[1],
					g.communityCards[0],
					g.communityCards[1],
					g.communityCards[2],
					g.communityCards[3],
					g.communityCards[4],
				)
				// lower is better for the score
				if score < pots[i].WinningScore {
					pots[i].WinningScore = score
					pots[i].WinningPlayerNums = []uint{num}
					pots[i].WinningHand = hand
				} else if score == pots[i].WinningScore {
					pots[i].WinningPlayerNums = append(pots[i].WinningPlayerNums, num)
				}
			}

			for _, num := range pots[i].WinningPlayerNums {
				g.players[num].Stack += (pots[i].Amt / uint(len(pots[i].WinningPlayerNums)))
				//TODO: leave the remainder in the middle! (some money will disappear currently)
			}
		}

		//The above takes care of all the all-in side pots. One last pot for the non-all-in people

		var finalPot Pot

		finalPot.WinningScore = 8000

		for i, p := range g.players {
			if p.In && !p.allIn() {
				finalPot.Amt += p.TotalBet
				p.TotalBet = 0

				hand, score := BestFiveOfSeven(
					p.Cards[0],
					p.Cards[1],
					g.communityCards[0],
					g.communityCards[1],
					g.communityCards[2],
					g.communityCards[3],
					g.communityCards[4],
				)
				// lower is better for the score
				if score < finalPot.WinningScore {
					finalPot.WinningScore = score
					finalPot.WinningPlayerNums = []uint{uint(i)}
					finalPot.WinningHand = hand
				} else if score == finalPot.WinningScore {
					finalPot.WinningPlayerNums = append(finalPot.WinningPlayerNums, uint(i))
				}
			}
		}

		for _, p := range finalPot.WinningPlayerNums {
			g.players[p].Stack += (finalPot.Amt / uint(len(finalPot.WinningPlayerNums)))
		}

		if finalPot.Amt != 0 {
			g.pots = append(pots, finalPot)
		}

		g.resetForNextHand()

		// otherwise, just set betting to false so the dealer can deal the next part of the hand
	} else {
		g.setBetting(false)
	}
	return nil

}

//Exported functions related to game management (not "Actions")

func NewGame() *Game {
	newGame := Game{}

	newGame.flags = 0x01
	newGame.dealerNum = 0

	return &newGame
}

func AddPlayer(g *Game) uint {
	g.players = append(g.players, player{})
	g.players[len(g.players)-1].initialize()
	return uint(len(g.players) - 1)
}