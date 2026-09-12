# 변경 기록

## 2026-09-08

- **Update** 독립 검토 보강: 다중 반환 인수·vet 검사 보존, 테스트 없는 보조 package 제외, 복원 전 변조 감지, 링크 대상과 예상 밖 편집 보호를 재현 후 수정.
- **Update** 최종 검증: 전체 Go 패키지·연결 관련 race·module 무결성 검사 통과. 변경 대상 함수 124개 CRAP 기준 초과·측정 불가 0개. 실제 CLI는 원본 대조 2개·재실행 쌍 3개를 출력하며 비인증 종료 6 유지.
- **Update** go-mutesting 연결: 원본·변이 소스, 0개 후보를 포함한 전체 계획, 테스트 목록·실패 위치·반복 횟수 지문을 재실행 결과에 연결.
- **Update** typed runner: 직접 실패 호출과 하위 테스트 결과를 opt-in으로 기록. 다른 검사문이 번갈아 실패하는 경우와 모순된 기록은 검출 성공에서 제외. v1 JSON 유지.
- **Update** go-mutesting-adapter.md·index.md: 반복 검사 출력 항목과 직접 호출 지원 범위, 비인증 경계를 반영.
- **Creation** go-mutesting-adapter.md: 고정 외부 module 직접 연결, 제한 profile과 독립 probe의 사용법·미승인 경계 기록.
- **Update** Go 실행기: 자식 프로세스 그룹 종료, 이벤트 통로의 취소 가능한 읽기와 총 크기 제한을 회귀 테스트로 보강.
- **Update** index.md·제3자 고지: 연결 문서 색인과 go-mutesting MIT 라이선스 추가.

## 2026-09-03

- **Creation** SENTINEL_GO 문서 번들: OKF v0.2 init 골격 생성.
