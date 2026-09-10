package server

func (s *runnerService) processList() []string {
	return runningProcessNames(s.Store.List())
}

func runningProcessNames(procs []*Process) []string {
	list := make([]string, 0, len(procs))
	for _, p := range procs {
		if p.IsRunning() {
			list = append(list, p.Name)
		}
	}
	return list
}
