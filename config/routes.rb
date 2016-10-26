Rails.application.routes.draw do
  # For details on the DSL available within this file, see http://guides.rubyonrails.org/routing.html
  root to: "home#index"
  get '/profile' => 'users#show', as: :profile

  post '/budgets/:id/share' => 'budgets#share', as: 'share_budget'
  post '/budgets/:id/unshare' => 'budgets#unshare', as: 'unshare_budget'
  post '/users/:id/edit_profile' => 'users#update'

  resources :budgets
  resources :transacts
  devise_for :users, controllers: { sessions: 'users/sessions' }
end
