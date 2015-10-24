package consensus

import (
	"errors"
	"time"

	"github.com/NebulousLabs/Sia/build"
	"github.com/NebulousLabs/Sia/modules"
	"github.com/NebulousLabs/Sia/types"

	"github.com/NebulousLabs/bolt"
)

var (
	errDoSBlock        = errors.New("block is known to be invalid")
	errNoBlockMap      = errors.New("block map is not in database")
	errInconsistentSet = errors.New("consensus set is not in a consistent state")
	errOrphan          = errors.New("block has no known parent")
)

// validateHeader does some early, low computation verification on the block.
// Callers should not assume that validation will happen in a particular order.
func (cs *ConsensusSet) validateHeader(tx dbTx, b types.Block) error {
	// See if the block is known already.
	id := b.ID()
	_, exists := cs.dosBlocks[id]
	if exists {
		return errDoSBlock
	}

	// Check if the block is already known.
	blockMap := tx.Bucket(BlockMap)
	if blockMap == nil {
		return errNoBlockMap
	}
	if blockMap.Get(id[:]) != nil {
		return modules.ErrBlockKnown
	}

	// Check for the parent.
	parentID := b.ParentID
	parentBytes := blockMap.Get(parentID[:])
	if parentBytes == nil {
		return errOrphan
	}

	var parent processedBlock
	err := cs.marshaler.Unmarshal(parentBytes, &parent)
	if err != nil {
		return err
	}
	// Check that the timestamp is not too far in the past to be acceptable.
	minTimestamp := cs.blockRuleHelper.minimumValidChildTimestamp(blockMap, &parent)

	return cs.blockValidator.ValidateBlock(b, minTimestamp, parent.ChildTarget, parent.Height+1)
}

// addBlockToTree inserts a block into the blockNode tree by adding it to its
// parent's list of children. If the new blockNode is heavier than the current
// node, the blockchain is forked to put the new block and its parents at the
// tip. An error will be returned if block verification fails or if the block
// does not extend the longest fork.
func (cs *ConsensusSet) addBlockToTree(b types.Block) (revertedBlocks, appliedBlocks []*processedBlock, err error) {
	var nonExtending bool
	err = cs.db.Update(func(tx *bolt.Tx) error {
		pb, err := getBlockMap(tx, b.ParentID)
		if build.DEBUG && err != nil {
			panic(err)
		}
		currentNode := currentProcessedBlock(tx)
		newNode := cs.newChild(tx, pb, b)

		// modules.ErrNonExtendingBlock should be returned if the block does
		// not extend the current blockchain, however the changes from newChild
		// should be comitted (which means 'nil' must be returned). A flag is
		// set to indicate that modules.ErrNonExtending should be returned.
		nonExtending = !newNode.heavierThan(currentNode)
		if nonExtending {
			return nil
		}
		revertedBlocks, appliedBlocks, err = cs.forkBlockchain(tx, newNode)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if nonExtending {
		return nil, nil, modules.ErrNonExtendingBlock
	}
	return revertedBlocks, appliedBlocks, nil
}

// AcceptBlock will add a block to the state, forking the blockchain if it is
// on a fork that is heavier than the current fork. If the block is accepted,
// it will be relayed to connected peers. This function should only be called
// for new, untrusted blocks.
func (cs *ConsensusSet) AcceptBlock(b types.Block) error {
	cs.mu.Lock()

	// Start verification inside of a bolt View tx.
	err := cs.db.View(func(tx *bolt.Tx) error {
		// Do not accept a block if the database is inconsistent.
		if inconsistencyDetected(tx) {
			return errors.New("inconsistent database")
		}

		// Check that the header is valid. The header is checked first because it
		// is not computationally expensive to verify, but it is computationally
		// expensive to create.
		err := cs.validateHeader(boltTxWrapper{tx}, b)
		if err != nil {
			// If the block is in the near future, but too far to be acceptable, then
			// save the block and add it to the consensus set after it is no longer
			// too far in the future.
			if err == errFutureTimestamp {
				go func() {
					time.Sleep(time.Duration(b.Timestamp-(types.CurrentTimestamp()+types.FutureThreshold)) * time.Second)
					cs.AcceptBlock(b) // NOTE: Error is not handled.
				}()
			}
			return err
		}
		return nil
	})
	if err != nil {
		cs.mu.Unlock()
		return err
	}

	// Try adding the block to the block tree. This call will perform
	// verification on the block before adding the block to the block tree. An
	// error is returned if verification fails or if the block does not extend
	// the longest fork.
	revertedBlocks, appliedBlocks, err := cs.addBlockToTree(b)
	if err != nil {
		cs.mu.Unlock()
		return err
	}

	// Log the changes in the change log.
	var ce changeEntry
	for _, rn := range revertedBlocks {
		ce.revertedBlocks = append(ce.revertedBlocks, rn.Block.ID())
	}
	for _, an := range appliedBlocks {
		ce.appliedBlocks = append(ce.appliedBlocks, an.Block.ID())
	}
	cs.changeLog = append(cs.changeLog, ce)

	// Demote the lock and send the update to the subscribers.
	cs.mu.Demote()
	defer cs.mu.DemotedUnlock()
	if len(appliedBlocks) > 0 {
		cs.readlockUpdateSubscribers(ce)
	}

	// Sanity checks.
	if build.DEBUG {
		// If appliedBlocks is 0, revertedBlocks will also be 0.
		if len(appliedBlocks) == 0 && len(revertedBlocks) != 0 {
			panic("appliedBlocks and revertedBlocks are mismatched!")
		}
	}

	// Broadcast the new block to all peers.
	go cs.gateway.Broadcast("RelayBlock", b)

	return nil
}
