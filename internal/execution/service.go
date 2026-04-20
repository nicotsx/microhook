package execution

type Service struct{}

func New() *Service {
	return &Service{}
}

func (s *Service) Close() error {
	return nil
}
