class Budget < ApplicationRecord
  belongs_to :user
  has_many :transact, dependent: :destroy, inverse_of: :budget
	validates :name,  presence: true, length: { maximum: 50 }

  def authorized_user?(user_id)
    authorized_users.include? user_id 
  end

end
