class Budget < ApplicationRecord
  has_and_belongs_to_many :user, :join_table => :users_budgets
  has_many :transact, dependent: :destroy, inverse_of: :budget
	validates :name,  presence: true, length: { maximum: 50 }

  def avg_debit(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", false, DateTime.now-time).average(:amount)
  end

  def max_debit(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", false, DateTime.now-time).maximum(:amount)
  end

  def balance
    credits = self.transact.where(credit: true).sum(:amount)
    debits = self.transact.where(credit: false).sum(:amount)
    credits - debits
  end

end
