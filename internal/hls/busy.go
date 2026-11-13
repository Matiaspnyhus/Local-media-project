package hls

// Converting reports how many films are having their picture converted at
// this moment: sessions that convert the picture and whose ffmpeg is alive.
// Each of them takes its share of the PC, which whoever decides how the
// next film is played has to allow for.
//
// Sessions of the sign-in named by except are left out. A viewer watches
// one film at a time: whatever else they have open is the film they just
// left, or the same film before they changed its sound, and is about to
// be closed.
func (m *Manager) Converting(except string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sessions {
		if s.spec.Convert != nil && s.spec.Owner != except && s.run != nil && s.run.alive() {
			n++
		}
	}
	return n
}
