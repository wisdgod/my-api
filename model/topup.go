package model

type TopUp struct {
	Id         int     `json:"id"`
	UserId     int     `json:"user_id" gorm:"index"`
	Amount     int     `json:"amount"`
	Money      float64 `json:"money"`
	TradeNo    string  `json:"trade_no"`
	CreateTime int64   `json:"create_time"`
	Status     string  `json:"status"`
}

func (topUp *TopUp) Insert() error {
	return DB.Create(topUp).Error
}

func (topUp *TopUp) Update() error {
	return DB.Save(topUp).Error
}

func GetTopUpById(id int) *TopUp {
	var topUp *TopUp
	if err := DB.Where("id = ?", id).First(&topUp).Error; err != nil {
		return nil
	}
	return topUp
}

func GetTopUpByTradeNo(tradeNo string) *TopUp {
	var topUp *TopUp
	if err := DB.Where("trade_no = ?", tradeNo).First(&topUp).Error; err != nil {
		return nil
	}
	return topUp
}
