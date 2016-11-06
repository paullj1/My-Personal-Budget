# Place all the behaviors and hooks related to the matching controller here.
# All this logic will automatically be available in application.js.
# You can use CoffeeScript in this file: http://coffeescript.org/

setup_affix = ->
  if !document.getElementById('budgets') 
    return

  $('.collapse').on 'hidden.bs.collapse', toggle_affix
  $('.collapse').on 'shown.bs.collapse', toggle_affix
  return

toggle_affix = ->
  if $(window).height() < $(document).height()
    # Show the floating button
    $('#new-transact-float').removeClass('hidden').affix offset: 
      top: 0
      bottom: ->
          @bottom = $('.footer').outerHeight(true) +
                    $('.debug_dump').outerHeight(true) + 10
    $('#new-transact').addClass('hidden')
    $('#budgets-container').css('padding-bottom', 70)
  else
    # Show the static button
    $('#new-transact-float').addClass('hidden')
    $('#new-transact').removeClass('hidden')
    $('#budgets-container').css('padding-bottom', 0)

  budget_width = $('#budgets').outerWidth(true)
  $('#new-transact-float').css('width', budget_width)
  return

$(document).on 'turbolinks:load', setup_affix
