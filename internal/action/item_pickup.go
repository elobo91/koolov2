package action

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/nip"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
)

func itemFitsInventory(i data.Item) bool {
	invMatrix := context.Get().Data.Inventory.Matrix()

	for y := 0; y <= len(invMatrix)-i.Desc().InventoryHeight; y++ {
		for x := 0; x <= len(invMatrix[0])-i.Desc().InventoryWidth; x++ {
			freeSpace := true
			for dy := 0; dy < i.Desc().InventoryHeight; dy++ {
				for dx := 0; dx < i.Desc().InventoryWidth; dx++ {
					if invMatrix[y+dy][x+dx] {
						freeSpace = false
						break
					}
				}
				if !freeSpace {
					break
				}
			}

			if freeSpace {
				return true
			}
		}
	}

	return false
}

func ItemPickup(maxDistance int) error {
	ctx := context.Get()
	ctx.ContextDebug.LastAction = "ItemPickup"

	for {
		itemsToPickup := GetItemsToPickup(maxDistance)
		if len(itemsToPickup) == 0 {
			return nil
		}

		itemToPickup := data.Item{}
		for _, i := range itemsToPickup {
			if itemFitsInventory(i) {
				itemToPickup = i
				break
			}
		}

		if itemToPickup.UnitID == 0 {
			ctx.Logger.Debug("Inventory is full, returning to town to sell junk and stash items")
			InRunReturnTownRoutine()
			continue
		}

		for _, m := range ctx.Data.Monsters.Enemies() {
			if _, dist, _ := ctx.PathFinder.GetPathFrom(itemToPickup.Position, m.Position); dist <= 3 {
				ctx.Logger.Debug("Monsters detected close to the item being picked up, killing them...", slog.Any("monster", m))
				_ = ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
					return m.UnitID, true
				}, nil)
				continue
			}
		}

		ctx.Logger.Debug(fmt.Sprintf(
			"Item Detected: %s [%d] at X:%d Y:%d",
			itemToPickup.Name,
			itemToPickup.Quality,
			itemToPickup.Position.X,
			itemToPickup.Position.Y,
		))

		err := MoveToCoords(itemToPickup.Position)
		if err != nil {
			ctx.Logger.Warn("Failed moving closer to item, trying to pickup it anyway", err)
		}

		err = step.PickupItem(itemToPickup)
		if errors.Is(err, step.ErrItemTooFar) {
			ctx.Logger.Debug("Item is too far away, retrying...")
			continue
		}
		if err != nil {
			ctx.CurrentGame.BlacklistedItems = append(ctx.CurrentGame.BlacklistedItems, itemToPickup)
			ctx.Logger.Warn(
				"Failed picking up item, blacklisting it",
				err.Error(),
				slog.String("itemName", itemToPickup.Desc().Name),
				slog.Int("unitID", int(itemToPickup.UnitID)),
			)
		}
	}
}

func GetItemsToPickup(maxDistance int) []data.Item {
    ctx := context.Get()
    ctx.ContextDebug.LastStep = "GetItemsToPickup"

    missingHealingPotions := ctx.BeltManager.GetMissingCount(data.HealingPotion)
    missingManaPotions := ctx.BeltManager.GetMissingCount(data.ManaPotion)
    missingRejuvenationPotions := ctx.BeltManager.GetMissingCount(data.RejuvenationPotion)

    var itemsToPickup []data.Item
    _, isLevelingChar := ctx.Char.(context.LevelingCharacter)

    for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationGround) {
		// Skip itempickup on party leveling Maggot Lair, is too narrow and causes characters to get stuck
		if isLevelingChar && !itm.IsFromQuest() && (ctx.Data.PlayerUnit.Area == area.MaggotLairLevel1 || ctx.Data.PlayerUnit.Area == area.MaggotLairLevel2 || ctx.Data.PlayerUnit.Area == area.MaggotLairLevel3 || ctx.Data.PlayerUnit.Area == area.ArcaneSanctuary) {
			continue
		}

		// Skip potion pickup for Berserker Barb in Travincal if configured
		if ctx.CharacterCfg.Character.Class == "berserker" &&
			ctx.CharacterCfg.Character.BerserkerBarb.SkipPotionPickupInTravincal &&
			ctx.Data.PlayerUnit.Area == area.Travincal &&
			itm.IsPotion() {
			continue
		}
		
		// Skip items that are outside pickup radius, this is useful when clearing big areas
        itemDistance := ctx.PathFinder.DistanceFromMe(itm.Position)
        if maxDistance > 0 && itemDistance > maxDistance {
            continue
        }

        if itm.IsPotion() {
            switch {
            case itm.IsHealingPotion() && missingHealingPotions > 0:
                itemsToPickup = append(itemsToPickup, itm)
                missingHealingPotions--
            case itm.IsManaPotion() && missingManaPotions > 0:
                itemsToPickup = append(itemsToPickup, itm)
                missingManaPotions--
            case itm.IsRejuvPotion() && missingRejuvenationPotions > 0:
                itemsToPickup = append(itemsToPickup, itm)
                missingRejuvenationPotions--
            }
        } else if shouldBePickedUp(itm) {
            itemsToPickup = append(itemsToPickup, itm)
        }
    }

    filteredItems := make([]data.Item, 0, len(itemsToPickup))
	
	// Remove blacklisted items from the list, we don't want to pick them up
    for _, itm := range itemsToPickup {
        isBlacklisted := false
        for _, blacklistedItem := range ctx.CurrentGame.BlacklistedItems {
            if itm.UnitID == blacklistedItem.UnitID {
                isBlacklisted = true
                break
            }
        }
        if !isBlacklisted {
            filteredItems = append(filteredItems, itm)
        }
    }

    return filteredItems
}

func shouldBePickedUp(i data.Item) bool {
    ctx := context.Get()
    ctx.ContextDebug.LastStep = "shouldBePickedUp"

    // Always pickup Runewords and Wirt's Leg
    if i.IsRuneword || i.Name == "WirtsLeg" {
        return true
    }

    // Skip picking up gold if we can not carry more
    if i.Name == "Gold" {
        gold, _ := ctx.Data.PlayerUnit.FindStat(stat.Gold, 0)
        if gold.Value >= ctx.Data.PlayerUnit.MaxGold() {
            ctx.Logger.Debug("Skipping gold pickup, inventory full")
            return false
        }
        return true
    }

    // Handle potion pickup
    if i.IsPotion() {
        // Skip potion pickup for Berserker Barb in Travincal if configured
        if ctx.CharacterCfg.Character.Class == "berserker" &&
            ctx.CharacterCfg.Character.BerserkerBarb.SkipPotionPickupInTravincal &&
            ctx.Data.PlayerUnit.Area == area.Travincal {
            return false
        }

        // Check if we need this type of potion
        switch {
        case i.IsHealingPotion():
            return ctx.BeltManager.GetMissingCount(data.HealingPotion) > 0
        case i.IsManaPotion():
            return ctx.BeltManager.GetMissingCount(data.ManaPotion) > 0
        case i.IsRejuvPotion():
            return ctx.BeltManager.GetMissingCount(data.RejuvenationPotion) > 0
        }
        return false
    }

    // Pick up quest items if we're in leveling or questing run
    specialRuns := slices.Contains(ctx.CharacterCfg.Game.Runs, "quests") || slices.Contains(ctx.CharacterCfg.Game.Runs, "leveling")
    if specialRuns {
        switch i.Name {
        case "Scrollofinifuss", "LamEsensTome", "HoradricCube", "AmuletoftheViper", "StaffofKings", "HoradricStaff", "AJadeFigurine", "KhalimsEye", "KhalimsBrain", "KhalimsHeart", "KhalimsFlail":
            return true
        }
    }
	
    if i.ID == 552 { // Book of Skill
        return true
    }

	// Only during leveling if gold amount is low pickup items to sell as junk
    _, isLevelingChar := ctx.Char.(context.LevelingCharacter)
	
	 // Skip picking up gold, usually early game there are small amounts of gold in many places full of enemies, better
	// stay away of that
  	if isLevelingChar && ctx.Data.PlayerUnit.TotalPlayerGold() < 50000 && i.Name != "Gold" {
		return true
	}

	// Pickup all magic or superior items if total gold is low, filter will not pass and items will be sold to vendor
    minGoldPickupThreshold := ctx.CharacterCfg.Game.MinGoldPickupThreshold
    if ctx.Data.PlayerUnit.TotalPlayerGold() < minGoldPickupThreshold && i.Quality >= item.QualityMagic {
        return true
    }

    // Evaluate item based on NIP rules
    matchedRule, result := ctx.Data.CharacterCfg.Runtime.Rules.EvaluateAll(i)
    if result == nip.RuleResultNoMatch {
        return false
    }
    if result == nip.RuleResultPartial {
        return true
    }
    return !doesExceedQuantity(matchedRule)
}
