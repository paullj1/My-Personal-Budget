class User < ApplicationRecord
  # Include default devise modules. Others available are:
  # :confirmable, :lockable, :timeoutable and :omniauthable
  devise :database_authenticatable, :registerable,
         :recoverable, :rememberable, :trackable, :validatable
  has_and_belongs_to_many :budget, :join_table => :users_budgets

  def budget_select
    self.budget.select(:name, :id).map { |u| [u.name, u.id] }
  end
end
