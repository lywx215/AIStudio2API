package aistudio

import "context"

// AcquireAccountChannel creates an exclusive lease for an isolated management
// operation. Unlike model routing it never chooses another account or channel.
func (p *AccountPool) AcquireAccountChannel(ctx context.Context, accountID string, channel Channel) (*AccountLease, error) {
	if channel != ChannelBuild && channel != ChannelPlayground {
		return nil, ErrInvalidArgument
	}
	p.mu.Lock()
	enabled := p.channelEnabledLocked(channel)
	p.mu.Unlock()
	if !enabled {
		return nil, ErrInvalidArgument
	}
	lease, err := p.AcquireAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	lease.channel = channel
	return lease, nil
}
